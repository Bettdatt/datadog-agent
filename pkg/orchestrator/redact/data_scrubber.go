// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//nolint:revive // TODO(CAPP) Fix revive linter
package redact

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	// regexSensitiveParamInJSON is using non greedy operators in the value capture
	// group to work around missing support for look behind assertions in Go.
	regexSensitiveParamInJSON = `(?P<before_value>"%s"\s*:\s*)(?P<value>".*?[^\\]+?")`

	redactedAnnotationValue = "-"
	redactedSecret          = "********"
)

var (
	defaultSensitiveWords = []string{
		"password", "passwd", "mysql_pwd",
		"access_token", "auth_token",
		"api_key", "apikey", "pwd",
		"secret", "credentials", "stripetoken"}
)

// DataScrubber allows the agent to block cmdline arguments that match
// a list of predefined and custom words
type DataScrubber struct {
	Enabled bool
	// RegexSensitivePatterns are custom regex patterns which are currently not exposed externally
	RegexSensitivePatterns []*regexp.Regexp
	// LiteralSensitivePatterns are custom words which use to match against
	LiteralSensitivePatterns         []string
	regexSensitiveWordsInAnnotations []*regexp.Regexp
	scrubbedCmdLines                 map[string][]string
}

// NewDefaultDataScrubber creates a DataScrubber with the default behavior: enabled
// and matching the default sensitive words
func NewDefaultDataScrubber() *DataScrubber {
	newDataScrubber := &DataScrubber{
		Enabled:                  true,
		LiteralSensitivePatterns: defaultSensitiveWords,
		scrubbedCmdLines:         make(map[string][]string),
	}

	newDataScrubber.setupAnnotationRegexps(defaultSensitiveWords)

	return newDataScrubber
}

func (ds *DataScrubber) setupAnnotationRegexps(words []string) {
	for _, word := range words {
		r := regexp.MustCompile(fmt.Sprintf(regexSensitiveParamInJSON, regexp.QuoteMeta(word)))
		ds.regexSensitiveWordsInAnnotations = append(ds.regexSensitiveWordsInAnnotations, r)
	}
}

// ContainsSensitiveWord returns true if the given string contains
// a sensitive word
func (ds *DataScrubber) ContainsSensitiveWord(s string) bool {
	for _, pattern := range ds.LiteralSensitivePatterns {
		if strings.Contains(strings.ToLower(s), pattern) {
			return true
		}
	}
	return false
}

// ScrubAnnotationValue obfuscate sensitive information from an annotation
// value.
func (ds *DataScrubber) ScrubAnnotationValue(annotationValue string) string {
	for _, r := range ds.regexSensitiveWordsInAnnotations {
		if r.MatchString(annotationValue) {
			annotationValue = r.ReplaceAllString(annotationValue, fmt.Sprintf(`${before_value}"%s"`, redactedSecret))
		}
	}
	return annotationValue
}

// ScrubSimpleCommand hides the argument value for any key which matches a "sensitive word" pattern.
// It returns the updated cmdline, as well as a boolean representing whether it was scrubbed.
func (ds *DataScrubber) ScrubSimpleCommand(cmd, args []string) ([]string, []string, bool) {
	if len(cmd) == 0 && len(args) == 0 {
		return cmd, args, false
	}

	changed := false
	regexChanged := false
	var wordReplacesIndexes []int

	separator := "cmd_separator"
	cmdline := append(cmd, separator)
	cmdline = append(cmdline, args...)

	// in case we have custom regexes we have to join them and perform regex find and replace
	rawCmdline := strings.Join(cmdline, " ")
	for _, pattern := range ds.RegexSensitivePatterns {
		if pattern.MatchString(rawCmdline) {
			regexChanged = true
			rawCmdline = pattern.ReplaceAllString(rawCmdline, fmt.Sprintf(`${key}${delimiter}%s`, redactedSecret))
		}
	}

	// Regex tokenizes by capturing non-whitespace terms as tokens EX: "agent --secret" > ["agent", "--secret"]
	// and non-whitespace terms followed by quotation enclosed subcomponents as tokens EX: "agent --pass="secret house"" > ["agent", "--pass="secret house""]
	r := regexp.MustCompile(`([^\s"']+("([^"]*)")*('([^']*)')*)`)
	newCmdline := r.FindAllString(rawCmdline, -1)

	// remove the separator from the cmdline
	cmIndex := 0
	for i := 0; i < len(newCmdline); i++ {
		if newCmdline[i] == separator {
			cmIndex = i
			break
		}
	}
	if cmIndex == len(newCmdline)-1 {
		newCmdline = newCmdline[:cmIndex]
	} else {
		newCmdline = append(newCmdline[:cmIndex], newCmdline[cmIndex+1:]...)
	}

	// preprocess, without the preprocessing we would need to strip until whitespaces.
	// the first index can be skipped because it should be the program name.
	for index := 1; index < len(newCmdline); index++ {
		cmd := newCmdline[index]
		for _, pattern := range ds.LiteralSensitivePatterns {
			// if we found a word from the list,
			// it means either:
			// - the current after a delimiter should be a password we want to replace e.g. "agent --secret=<replace-me>" (1)
			// - the next word should be replaced.  e.g. "agent --secret <replace-me>" (1)
			// - the word is part of a special list of words, contains multiple supportedEnds and can be ignored e.g. "agent > /secret/secret" should not match (3)
			matchIndex := strings.Index(strings.ToLower(cmd), pattern)
			// password<delimiter>1234 || password || (ignore<delimiter>me<delimiter>password e.g ignore:me:password ignore/me/password)
			// agent --password======test
			// agent > /password/secret ==> agent > /password/secret
			// agent --password > /password/secret ==> agent > --password ******** /password/secret

			if matchIndex >= 0 {
				before := cmd[:matchIndex] // /etc/vaultd/ from /etc/vaultd/secret/haproxy-crt.pem
				// skip paths /etc/vaultd/secrets/haproxy-crt.pem -> we don't want to match if one of the below chars are in before
				if strings.ContainsAny(before, "/:=$") {
					break
				}

				changed = true
				v := strings.IndexAny(cmd, "=:")
				if v >= 0 {
					// password:1234  password=1234 ==> password=****** || password:******
					// password::::====1234 ==> password:******
					newCmdline[index] = cmd[:v+1] + redactedSecret
					// replace from v to end of string with ********
					break
				} else { //nolint:revive // TODO(CAPP) Fix revive linter
					// password 1234 password ******
					nextReplacementIndex := index + 1
					if nextReplacementIndex < len(newCmdline) {
						wordReplacesIndexes = append(wordReplacesIndexes, nextReplacementIndex)
						index++
					}
					break
				}
			}
		}
	}

	for _, index := range wordReplacesIndexes {
		// we only want to replace words, hence we jump to the next index and try to scrub that instead
		for index < len(newCmdline) && newCmdline[index] == "" {
			index++
		}
		if index < len(newCmdline) {
			newCmdline[index] = redactedSecret
		}
	}

	// if nothing changed, just return the input
	if !(changed || regexChanged) {
		return cmd, args, false
	}

	return newCmdline[:cmIndex], newCmdline[cmIndex:], changed || regexChanged
}

// AddCustomSensitiveWords adds custom sensitive words on the DataScrubber object
func (ds *DataScrubber) AddCustomSensitiveWords(words []string) {
	ds.LiteralSensitivePatterns = append(ds.LiteralSensitivePatterns, words...)
	ds.setupAnnotationRegexps(words)
}

// AddCustomSensitiveRegex adds custom sensitive regex on the DataScrubber object
func (ds *DataScrubber) AddCustomSensitiveRegex(words []string) {
	r := compileStringsToRegex(words)
	ds.RegexSensitivePatterns = append(ds.RegexSensitivePatterns, r...)
}

// compileStringsToRegex compile each word in the slice into a regex pattern to match
// against the cmdline arguments (originally imported from pkg/process/config)
// The word must contain only word characters ([a-zA-z0-9_]) or wildcards *
func compileStringsToRegex(words []string) []*regexp.Regexp {
	compiledRegexps := make([]*regexp.Regexp, 0, len(words))
	forbiddenSymbols := regexp.MustCompile("[^a-zA-Z0-9_*]")

	for _, word := range words {
		if forbiddenSymbols.MatchString(word) {
			log.Warnf("data scrubber: %s skipped. The sensitive word must "+
				"contain only alphanumeric characters, underscores or wildcards ('*')", word)
			continue
		}

		if word == "*" {
			log.Warn("data scrubber: ignoring wildcard-only ('*') sensitive word as it is not supported")
			continue
		}

		originalRunes := []rune(word)
		var enhancedWord bytes.Buffer
		valid := true
		for i, rune := range originalRunes {
			if rune == '*' {
				if i == len(originalRunes)-1 {
					enhancedWord.WriteString("[^ =:]*")
				} else if originalRunes[i+1] == '*' {
					log.Warnf("data scrubber: %s skipped. The sensitive word "+
						"must not contain two consecutives '*'", word)
					valid = false
					break
				} else {
					enhancedWord.WriteString("[^\\s=:$/]*")
				}
			} else {
				enhancedWord.WriteString(string(rune))
			}
		}

		if !valid {
			continue
		}

		pattern := "(?P<key>( +| -{1,2})(?i)" + enhancedWord.String() + ")(?P<delimiter> +|=|:)(?P<value>[^\\s]*)"
		r, err := regexp.Compile(pattern)
		if err == nil {
			compiledRegexps = append(compiledRegexps, r)
		} else {
			log.Warnf("data scrubber: %s skipped. It couldn't be compiled into a regex expression", word)
		}
	}

	return compiledRegexps
}
