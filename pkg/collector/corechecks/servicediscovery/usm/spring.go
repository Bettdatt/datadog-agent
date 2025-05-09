// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package usm

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/rickar/props"
	"github.com/vibrantbyte/go-antpath/antpath"

	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/servicediscovery/envs"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	bootInfJarPath = "BOOT-INF/classes/"

	defaultLocations       = "optional:classpath:/;optional:classpath:/config/;optional:file:./;optional:file:./config/;optional:file:./config/*/"
	defaultConfigName      = "application"
	locationPropName       = "spring.config.locations"
	configPropName         = "spring.config.name"
	activeProfilesPropName = "spring.profiles.active"

	appnamePropName = "spring.application.name"

	springBootLauncher    = "org.springframework.boot.loader.launch.JarLauncher"
	springBootOldLauncher = "org.springframework.boot.loader.JarLauncher"
)

// mapSource is a type holding properties stored as map. It implements PropertyGetter
type mapSource struct {
	m map[string]string
}

// Get the value for the supplied key
func (y *mapSource) Get(key string) (string, bool) {
	val, ok := y.m[key]
	return val, ok
}

// GetDefault gets the value for the supplied key or the defVal if missing
func (y *mapSource) GetDefault(key string, defVal string) string {
	val, ok := y.m[key]
	if !ok {
		return defVal
	}
	return val
}

// newArgumentSource a PropertyGetter that is taking key=value from the list of arguments provided
// it can be done to parse both java system properties (the prefix is `-D`) or spring boot property args (the prefix is `--`)
func newArgumentSource(arguments []string, prefix string) props.PropertyGetter {
	parsed := make(map[string]string)
	for _, val := range arguments {
		if !strings.HasPrefix(val, prefix) {
			continue
		}
		if key, value, hasValue := strings.Cut(val[len(prefix):], "="); hasValue {
			parsed[key] = value
		} else {
			parsed[key] = ""
		}
	}
	return &mapSource{parsed}
}

type environmentSource struct {
	m props.PropertyGetter
}

// Get the value for the supplied key
func (y *environmentSource) Get(key string) (string, bool) {
	return y.m.Get(strings.Map(normalizeEnv, key))
}

// GetDefault gets the value for the supplied key or the defVal if missing
func (y *environmentSource) GetDefault(key string, defVal string) string {
	return y.m.GetDefault(strings.Map(normalizeEnv, key), defVal)
}
func newEnvironmentSource(envs envs.Variables) props.PropertyGetter {
	return &environmentSource{m: &envs}
}

// normalizeEnv converts a rune into a suitable replacement for an environment variable name.
func normalizeEnv(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	} else if r >= 'A' && r <= 'Z' {
		return r
	} else if r >= '0' && r <= '9' {
		return r
	}

	return '_'
}

type springBootParser struct {
	ctx DetectionContext
}

func newSpringBootParser(ctx DetectionContext) *springBootParser {
	return &springBootParser{ctx: ctx}
}

// parseURI parses locations (usually specified by the property locationPropName) given the list of active profiles (specified by activeProfilesPropName)
// It returns a couple of maps each having as key the profile name ("" stands for default one) and as value the ant patterns where the properties should be found
// The first map returned is the locations to be found in fs while the second map contains locations on the classpath (usually inside the application jar)
func (s springBootParser) parseURI(locations []string, name string, profiles []string) (map[string][]string, map[string][]string) {
	classpaths := make(map[string][]string)
	files := make(map[string][]string)
	for _, current := range locations {
		parts := strings.Split(current, ":")
		pl := len(parts)

		isClasspath := false
		if pl > 1 && parts[pl-2] == "classpath" {
			parts[pl-1] = bootInfJarPath + parts[pl-1]
			isClasspath = true
		}
		parts[pl-1] = filepath.ToSlash(parts[pl-1])

		doAppend := func(name string, profile string) {
			name = path.Clean(name)
			if isClasspath {
				classpaths[profile] = append(classpaths[profile], name)
			} else {
				files[profile] = append(files[profile], s.ctx.resolveWorkingDirRelativePath(name))
			}
		}
		if strings.HasSuffix(parts[pl-1], "/") {
			// we have a path: add all the possible filenames
			tmp := parts[pl-1] + name
			// there is an extension based priority also: first properties then yaml
			for _, profile := range profiles {
				tmp2 := tmp + "-" + profile
				for _, ext := range []string{".properties", ".yaml", ".yml"} {
					doAppend(tmp2+ext, profile)
				}
			}
			for _, ext := range []string{".properties", ".yaml", ".yml"} {
				doAppend(tmp+ext, "")
			}
		} else {
			// just add it since it's a direct file
			doAppend(parts[pl-1], "")
		}
	}
	return files, classpaths
}

// newPropertySourceFromStream create a PropertyGetter by selecting the most appropriate parser giving the file extension.
// An error will be returned if the filesize is greater than maxParseFileSize
func newPropertySourceFromStream(rc io.Reader, filename string, filesize uint64) (props.PropertyGetter, error) {
	if filesize > maxParseFileSize {
		return nil, fmt.Errorf("unable to parse %q. max file size exceeded(actual: %d, max: %d)", filename, filesize, maxParseFileSize)
	}
	var properties props.PropertyGetter
	var err error
	ext := strings.ToLower(path.Ext(filename))
	switch ext {
	case ".properties":
		properties, err = props.Read(rc)
	case ".yaml", ".yml":
		properties, err = newYamlSource(rc)
	default:
		return nil, fmt.Errorf("unhandled file type for %q", filename)
	}
	return properties, err
}

// newPropertySourceFromFile wraps filename opening and closing, delegating the rest of the logic to newPropertySourceFromStream
func (s springBootParser) newPropertySourceFromFile(filename string) (props.PropertyGetter, error) {
	f, err := s.ctx.fs.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	reader, err := SizeVerifiedReader(f)
	if err != nil {
		return nil, err
	}
	return newPropertySourceFromStream(reader, filename, uint64(fi.Size()))
}

// longestPathPrefix extracts the longest path's portion that's not a pattern (i.e. /test/**/*.xml will return /test/)
func longestPathPrefix(pattern string) string {
	idx := strings.IndexAny(pattern, "?*")
	if idx < 0 {
		return pattern
	}
	idx = strings.LastIndex(pattern[:idx], "/")
	if idx < 0 {
		return ""
	}
	return pattern[:idx]
}

// scanSourcesFromFileSystem returns all the PropertyGetter sources built from files matching profilePatterns.
// profilePatterns is a map that has for key the name of the spring profile and for key the values of patterns to be evaluated to find those files
func (s springBootParser) scanSourcesFromFileSystem(profilePatterns map[string][]string) map[string]*props.Combined {
	ret := make(map[string]*props.Combined)
	matcher := antpath.New()
	for profile, pp := range profilePatterns {
		for _, pattern := range pp {
			startPath := longestPathPrefix(pattern)
			_ = fs.WalkDir(s.ctx.fs, startPath, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !matcher.MatchStart(pattern, p) {
					if d.IsDir() {
						// skip the whole directory subtree since the prefix does not match
						return fs.SkipDir
					}
					// skip the current file
					return nil
				}
				// a match is found
				value, err := s.newPropertySourceFromFile(p)
				if err != nil {
					log.Debugf("cannot parse a property source (filename: %q). Err: %v", p, err)
					return nil
				}
				arr, ok := ret[profile]
				if !ok {
					arr = &props.Combined{Sources: []props.PropertyGetter{}}
					ret[profile] = arr
				}
				arr.Sources = append(arr.Sources, value)

				return nil
			})
		}
	}
	return ret
}

// newPropertySourceFromInnerJarFile opens a file inside a zip archive and returns a PropertyGetter or error if unable to handle the file
func newPropertySourceFromInnerJarFile(f *zip.File) (props.PropertyGetter, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return newPropertySourceFromStream(rc, f.Name, f.UncompressedSize64)
}

// newSpringBootArchiveSourceFromReader return a PropertyGetter combined source with properties sources from the jar application.
func newSpringBootArchiveSourceFromReader(reader *zip.Reader, patternMap map[string][]string) map[string]*props.Combined {
	ret := make(map[string]*props.Combined)
	matcher := antpath.New()
	for _, f := range reader.File {
		name := f.Name
		// the generalized approach implies visiting also jar in BOOT-INF/lib but here we skip it
		// to minimize the scanning time given that the general habit is to package config
		// directly into the application and not in a lib embedded into the app
		if !strings.HasPrefix(name, bootInfJarPath) {
			continue
		}
		for profile, patterns := range patternMap {
			for _, pattern := range patterns {
				if matcher.Match(pattern, name) {
					source, err := newPropertySourceFromInnerJarFile(f)
					if err != nil {
						break
					}
					val, ok := ret[profile]
					if !ok {
						val = &props.Combined{Sources: []props.PropertyGetter{}}
						ret[profile] = val
					}
					val.Sources = append(val.Sources, source)
					break
				}
			}
		}
	}
	return ret
}

// GetSpringBootAppName tries to autodetect the name of a spring boot application given its working dir,
// the jar path and the application arguments.
// When resolving properties, it supports placeholder resolution (a = ${b} -> will lookup then b)
func (s springBootParser) GetSpringBootAppName(jarname string) (string, bool) {
	absName := s.ctx.resolveWorkingDirRelativePath(jarname)

	file, err := s.ctx.fs.Open(absName)
	if err != nil {
		return "", false
	}
	defer file.Close()
	reader, err := VerifiedZipReader(file)
	if err != nil {
		return "", false
	}

	log.Debugf("parsing information from spring boot archive: %q", absName)

	return s.getSpringBootAppNameFromJar(reader)
}

func (s springBootParser) getSpringBootAppNameFromJar(reader *zip.Reader) (string, bool) {
	if !isSpringBootArchive(reader) {
		return "", false
	}

	return s.getSpringBootAppNameWithReader(func(patterns map[string][]string) map[string]*props.Combined {
		return newSpringBootArchiveSourceFromReader(reader, patterns)
	})
}

type classpathSourcesCallback func(map[string][]string) map[string]*props.Combined

func (s springBootParser) getSpringBootAppNameWithReader(getClasspathSources classpathSourcesCallback) (string, bool) {
	combined := &props.Combined{Sources: []props.PropertyGetter{
		newArgumentSource(s.ctx.Args, "--"),
		newArgumentSource(s.ctx.Args, "-D"),
		newEnvironmentSource(s.ctx.Envs),
	}}

	// resolved properties referring to other properties (thanks to the Expander)
	conf := &props.Configuration{Props: props.NewExpander(combined)}
	// Looking in the environment (sysprops, arguments) first
	appname, ok := conf.Get(appnamePropName)
	if ok {
		return appname, true
	}

	// otherwise look in the fs and inside the jar
	locations := strings.Split(combined.GetDefault(locationPropName, defaultLocations), ";")
	confname := combined.GetDefault(configPropName, defaultConfigName)
	var profiles []string
	rawProfile, ok := combined.Get(activeProfilesPropName)
	if ok && len(rawProfile) > 0 {
		profiles = strings.Split(rawProfile, ",")
	}
	files, classpaths := s.parseURI(locations, confname, profiles)
	fileSources := s.scanSourcesFromFileSystem(files)
	classpathSources := getClasspathSources(classpaths)
	//assemble by profile
	for _, profile := range append(profiles, "") {
		if val, ok := fileSources[profile]; ok {
			combined.Sources = append(combined.Sources, val)
		}
		if val, ok := classpathSources[profile]; ok {
			combined.Sources = append(combined.Sources, val)
		}
	}
	return conf.Get(appnamePropName)
}

func (s springBootParser) getSpringBootAppNameFromUnpackedJar(abspath string) (string, bool) {
	log.Debugf("parsing information from unpacked jar at %q", abspath)

	return s.getSpringBootAppNameWithReader(func(patterns map[string][]string) map[string]*props.Combined {
		for profile := range patterns {
			for i := range patterns[profile] {
				patterns[profile][i] = path.Join(abspath, patterns[profile][i])
			}
		}
		return s.scanSourcesFromFileSystem(patterns)
	})
}

// GetSpringBootLauncherAppName gets the name of the application if it is
// launched via the Spring Boot JarLauncher.
func (s springBootParser) GetSpringBootLauncherAppName() (string, bool) {
	classPath := getClassPath(s.ctx.Args)
	if len(classPath) == 0 {
		return "", false
	}

	// To limit the amount of processing, we only support the most common case
	// of having the files in the first entry in the classpath (or in the
	// default classpath if not explicit classpath is specified).
	basePath := s.ctx.resolveWorkingDirRelativePath(classPath[0])

	var name string
	var ok bool
	var fs fs.FS
	var manifestPath string

	isJar := path.Ext(basePath) == ".jar"
	if isJar {
		file, err := s.ctx.fs.Open(basePath)
		if err != nil {
			return "", false
		}
		defer file.Close()

		reader, err := VerifiedZipReader(file)
		if err != nil {
			return "", false
		}

		name, ok = s.getSpringBootAppNameFromJar(reader)
		fs = reader
		manifestPath = manifestFile
	} else {
		name, ok = s.getSpringBootAppNameFromUnpackedJar(basePath)
		fs = s.ctx.fs
		manifestPath = path.Join(basePath, manifestFile)
	}
	if ok {
		return name, true
	}

	// Could not find the application name from properties, at least try to get
	// the real Start-Class to avoid reporting the name as the generic
	// JarLauncher.
	name, err := getStartClassName(fs, manifestPath)
	if err == nil {
		return name, true
	}

	return "", false
}

// isSpringBootArchive heuristically determines if a jar archive is a spring boot packaged jar
func isSpringBootArchive(reader *zip.Reader) bool {
	for _, f := range reader.File {
		if strings.HasPrefix(f.Name, "BOOT-INF/") {
			return true
		}
	}
	return false
}
