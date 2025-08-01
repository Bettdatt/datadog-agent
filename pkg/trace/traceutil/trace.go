// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package traceutil

import (
	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/trace"
	"github.com/DataDog/datadog-agent/pkg/trace/log"
)

const (
	envKey = "env"
)

// GetEnv returns the first "env" tag found in trace t.
// Search starts by root
func GetEnv(root *pb.Span, t *pb.TraceChunk) string {
	if v, ok := root.Meta[envKey]; ok {
		return v
	}
	for _, s := range t.Spans {
		if s.SpanID == root.SpanID {
			continue
		}
		if v, ok := s.Meta[envKey]; ok {
			return v
		}
	}
	return ""
}

// GetRoot extracts the root span from a trace
func GetRoot(t pb.Trace) *pb.Span {
	// That should be caught beforehand
	if len(t) == 0 {
		return nil
	}
	// General case: go over all spans and check for one which matching parent
	parentIDToChild := map[uint64]*pb.Span{}

	for i := range t {
		// Common case optimization: check for span with ParentID == 0, starting from the end,
		// since some clients report the root last
		j := len(t) - 1 - i
		if t[j].ParentID == 0 {
			return t[j]
		}
		parentIDToChild[t[j].ParentID] = t[j]
	}

	for i := range t {
		delete(parentIDToChild, t[i].SpanID)
	}

	// Here, if the trace is valid, we should have len(parentIDToChild) == 1
	if len(parentIDToChild) != 1 {
		log.Debugf("Didn't reliably find the root span for traceID:%v", t[0].TraceID)
	}

	// Have a safe behavior if that's not the case
	// Pick a random span without its parent
	for parentID := range parentIDToChild {
		return parentIDToChild[parentID]
	}

	// Gracefully fail with the last span of the trace
	return t[len(t)-1]
}

// ChildrenMap returns a map containing for each span id the list of its
// direct children.
func ChildrenMap(t pb.Trace) map[uint64][]*pb.Span {
	childrenMap := make(map[uint64][]*pb.Span)

	for i := range t {
		span := t[i]
		if span.ParentID == 0 {
			continue
		}
		childrenMap[span.ParentID] = append(childrenMap[span.ParentID], span)
	}

	return childrenMap
}

// ComputeTopLevel updates all the spans top-level attribute.
//
// A span is considered top-level if:
//   - it's a root span
//   - OR its parent is unknown (other part of the code, distributed trace)
//   - OR its parent belongs to another service (in that case it's a "local root"
//     being the highest ancestor of other spans belonging to this service and
//     attached to it).
func ComputeTopLevel(trace pb.Trace) {
	spanIDToIndex := make(map[uint64]int, len(trace))
	for i, span := range trace {
		spanIDToIndex[span.SpanID] = i
	}
	for _, span := range trace {
		if span.ParentID == 0 {
			// span is a root span
			SetTopLevel(span, true)
			continue
		}
		parentIndex, ok := spanIDToIndex[span.ParentID]
		if !ok {
			// span has no parent in chunk
			SetTopLevel(span, true)
			continue
		}
		if trace[parentIndex].Service != span.Service {
			// parent is not in the same service
			SetTopLevel(span, true)
			continue
		}
	}
}
