// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package tagger implements the Tagger component. The Tagger is the central
// source of truth for client-side entity tagging. It subscribes to workloadmeta
// to get updates for all the entity kinds (containers, kubernetes pods,
// kubernetes nodes, etc.) and extracts the tags for each of them. Tags are then
// stored in memory (by the TagStore) and can be queried by the tagger.Tag()
// method.

// Package noopimpl provides a noop implementation for the tagger component
package noopimpl

import (
	tagger "github.com/DataDog/datadog-agent/comp/core/tagger/def"
	"github.com/DataDog/datadog-agent/comp/core/tagger/origindetection"
	"github.com/DataDog/datadog-agent/comp/core/tagger/types"
	taggertypes "github.com/DataDog/datadog-agent/pkg/tagger/types"
	"github.com/DataDog/datadog-agent/pkg/tagset"
)

type noopTagger struct{}

func (n *noopTagger) Tag(types.EntityID, types.TagCardinality) ([]string, error) {
	return nil, nil
}

// GenerateContainerIDFromOriginInfo generates a container ID from Origin Info.
// This is a no-op for the noop tagger
func (n *noopTagger) GenerateContainerIDFromOriginInfo(origindetection.OriginInfo) (string, error) {
	return "", nil
}

func (n *noopTagger) AccumulateTagsFor(types.EntityID, types.TagCardinality, tagset.TagsAccumulator) error {
	return nil
}

func (n *noopTagger) Standard(types.EntityID) ([]string, error) {
	return nil, nil
}

func (n *noopTagger) List() types.TaggerListResponse {
	return types.TaggerListResponse{}
}

func (n *noopTagger) GetEntity(types.EntityID) (*types.Entity, error) {
	return nil, nil
}

func (n *noopTagger) Subscribe(string, *types.Filter) (types.Subscription, error) {
	return nil, nil
}

func (n *noopTagger) GetEntityHash(types.EntityID, types.TagCardinality) string {
	return ""
}

func (n *noopTagger) AgentTags(types.TagCardinality) ([]string, error) {
	return nil, nil
}

func (n *noopTagger) GlobalTags(types.TagCardinality) ([]string, error) {
	return nil, nil
}

func (n *noopTagger) EnrichTags(tagset.TagsAccumulator, taggertypes.OriginInfo) {}

// NewComponent returns a new noop tagger component
func NewComponent() tagger.Component {
	return &noopTagger{}
}
