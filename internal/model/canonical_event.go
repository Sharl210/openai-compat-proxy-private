package model

import (
	"encoding/json"
	"time"
)

type CanonicalEnvelope struct {
	RequestID        string
	UpstreamProtocol string
	ProviderID       string
	Model            string
	Events           []CanonicalEvent
	ProviderMeta     map[string]any
}

type CanonicalEvent struct {
	Seq            int64
	Ts             time.Time
	Type           string
	ItemID         string
	CallID         string
	Role           string
	TextDelta      string
	ReasoningDelta string
	ToolName       string
	ToolArgsDelta  string
	UsageDelta     map[string]any
	FinishReason   string
	Error          map[string]any
	RawEventName   string
	RawPayload     json.RawMessage
	ProviderMeta   map[string]any
}

func (e *CanonicalEnvelope) AppendEvent(evt CanonicalEvent) {
	e.Events = append(e.Events, evt)
}

func (e CanonicalEvent) Clone() CanonicalEvent {
	clone := e
	if e.UsageDelta != nil {
		clone.UsageDelta = make(map[string]any, len(e.UsageDelta))
		for k, v := range e.UsageDelta {
			clone.UsageDelta[k] = v
		}
	}
	if e.Error != nil {
		clone.Error = make(map[string]any, len(e.Error))
		for k, v := range e.Error {
			clone.Error[k] = v
		}
	}
	if e.ProviderMeta != nil {
		clone.ProviderMeta = make(map[string]any, len(e.ProviderMeta))
		for k, v := range e.ProviderMeta {
			clone.ProviderMeta[k] = v
		}
	}
	if e.RawPayload != nil {
		clone.RawPayload = make(json.RawMessage, len(e.RawPayload))
		copy(clone.RawPayload, e.RawPayload)
	}
	return clone
}

func (e CanonicalEnvelope) Clone() CanonicalEnvelope {
	clone := CanonicalEnvelope{
		RequestID:        e.RequestID,
		UpstreamProtocol: e.UpstreamProtocol,
		ProviderID:       e.ProviderID,
		Model:            e.Model,
		Events:           make([]CanonicalEvent, len(e.Events)),
		ProviderMeta:     nil,
	}
	for i, evt := range e.Events {
		clone.Events[i] = evt.Clone()
	}
	if e.ProviderMeta != nil {
		clone.ProviderMeta = make(map[string]any, len(e.ProviderMeta))
		for k, v := range e.ProviderMeta {
			clone.ProviderMeta[k] = v
		}
	}
	return clone
}
