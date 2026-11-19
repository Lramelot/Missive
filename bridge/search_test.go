package main

import (
	"strings"
	"testing"
)

func TestSearch(t *testing.T) {
	s := NewStore()
	s.SetSelfID(testSelf)
	for i, m := range []Message{
		{ID: "a", ThreadID: "1", Text: "Rendez-vous à l'ÉCOLE demain", TimestampMS: 100},
		{ID: "b", ThreadID: "2", Text: "ecole fermée", TimestampMS: 300},
		{ID: "c", ThreadID: "1", Attachments: []Attachment{{ID: "x", Kind: "link", Title: "École numérique"}}, TimestampMS: 200},
		{ID: "d", ThreadID: "1", Attachments: []Attachment{{ID: "y", Kind: "file", FileName: "Plan_ECOLE.pdf"}}, TimestampMS: 50},
		{ID: "e", ThreadID: "1", Text: "école", Deleted: true, TimestampMS: 400},
		{ID: "f", ThreadID: "1", Text: "rien à voir", TimestampMS: 500},
	} {
		_ = i
		s.PutMessage(m)
	}
	hits := s.Search("ÉcOle", "", 10)
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.Message.ID)
		if h.ThreadID != h.Message.ThreadID || h.Message.Reactions == nil {
			t.Fatalf("bad hit %+v", h)
		}
	}
	if strings.Join(ids, ",") != "b,c,a,d" {
		t.Fatalf("unexpected hits %v", ids)
	}
	if hits := s.Search("ecole", "1", 2); len(hits) != 2 || hits[0].Message.ID != "c" {
		t.Fatalf("thread filter / limit broken: %+v", hits)
	}
}
