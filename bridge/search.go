package main

import (
	"slices"
	"strings"
)

var searchKinds = []string{"image", "video", "audio", "voice", "file", "link", "location", "sticker", "contact", "story", "other"}

type SearchFilter struct {
	Query    string
	ThreadID string
	SenderID string
	Kinds    []string
	BeforeMS int64
	AfterMS  int64
}

func (f SearchFilter) filtered() bool {
	return f.ThreadID != "" || f.SenderID != "" || len(f.Kinds) > 0 || f.BeforeMS > 0 || f.AfterMS > 0
}

func (f SearchFilter) matches(msg *Message, needle, selfID string) bool {
	if f.BeforeMS > 0 && msg.TimestampMS >= f.BeforeMS {
		return false
	}
	if f.AfterMS > 0 && msg.TimestampMS < f.AfterMS {
		return false
	}
	if f.SenderID != "" && msg.SenderID != f.SenderID && !(msg.IsOutgoing && f.SenderID == selfID) {
		return false
	}
	if len(f.Kinds) > 0 && !messageHasKind(msg, f.Kinds) {
		return false
	}
	if needle == "" {
		return !msg.System
	}
	return strings.Contains(searchableText(msg), needle)
}

func messageHasKind(msg *Message, kinds []string) bool {
	for _, kind := range kinds {
		if kind == "link" && containsWebLink(msg.Text) {
			return true
		}
		for _, att := range msg.Attachments {
			switch {
			case kind == "voice" && att.Kind == "audio" && att.Voice:
				return true
			case att.Kind == kind:
				return true
			}
		}
	}
	return false
}

func containsWebLink(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "https://") || strings.Contains(lower, "http://")
}

func validSearchKinds(kinds []string) (string, bool) {
	for _, kind := range kinds {
		if !slices.Contains(searchKinds, kind) {
			return kind, false
		}
	}
	return "", true
}
