package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDemoSeedsThreadSettings(t *testing.T) {
	h := newDemoHarness(t)
	threads := h.demo.store.ListThreads(0)
	seen := map[string]bool{}
	for _, th := range threads {
		switch {
		case th.Folder == FolderRequests:
			seen["request"] = true
		case th.Folder == FolderArchived:
			seen["archived"] = true
		}
		if th.MutedUntilMS != nil && *th.MutedUntilMS > 0 {
			seen["muted_until"] = true
		}
		if th.Theme != nil && len(th.Theme.Gradient) > 1 && th.QuickEmoji != "" {
			seen["theme"] = true
		}
		if th.EphemeralTTLMS > 0 {
			seen["ephemeral"] = true
		}
		if len(th.PinnedMessageIDs) > 0 {
			seen["pinned"] = true
		}
		if th.IsAdmin && th.CanManage {
			seen["admin"] = true
		}
		if th.Blocked {
			seen["blocked"] = true
		}
		for _, r := range th.ReadReceipts {
			if r.DeliveredMS > 0 {
				seen["delivered"] = true
			}
		}
		for _, p := range th.Participants {
			if p.IsAdmin {
				seen["participant_admin"] = true
			}
		}
		for _, m := range h.demo.store.Messages(th.ID, 0, 0) {
			if m.System {
				seen["system"] = true
			}
			if m.Forwarded {
				seen["forwarded"] = true
			}
			if m.Poll != nil && m.Poll.TotalVotes > 0 {
				seen["poll"] = true
			}
			for _, a := range m.Attachments {
				seen[a.Kind] = true
				if a.Voice && len(a.Waveform) > 0 {
					res, err := h.demo.Media(context.Background(), th.ID, m.ID, a.ID, VariantFull)
					if info, statErr := os.Stat(res.Path); err != nil || statErr != nil || info.Size() < 1000 {
						t.Fatalf("demo voice must be playable: %+v %v", res, err)
					}
					seen["voice"] = true
				}
			}
		}
	}
	for _, want := range []string{"request", "archived", "muted_until", "theme", "ephemeral", "pinned", "admin", "blocked", "delivered", "participant_admin", "system", "forwarded", "poll", "location", "story", "contact", "voice"} {
		if !seen[want] {
			t.Errorf("demo data lacks %s", want)
		}
	}
}

func TestDemoMessageAndThreadCommands(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	coloc, peer := demoGroupID(1), demoPeople[0].ID
	h.send(fmt.Sprintf(`{"id":2,"cmd":"send","params":{"thread_id":%q,"text":"brouillon"}}`, coloc))
	var sent SendResult
	h.okResult(2, &sent)
	dir := t.TempDir()
	wav := filepath.Join(dir, "v.wav")
	os.WriteFile(wav, demoToneWAV(1, 16000), 0o600)
	img := filepath.Join(dir, "i.png")
	png, _ := demoPNG("x", 32, 32)
	os.WriteFile(img, png, 0o600)
	steps := []string{
		fmt.Sprintf(`{"id":3,"cmd":"edit_message","params":{"thread_id":%q,"message_id":%q,"text":"corrigé"}}`, coloc, sent.MessageID),
		fmt.Sprintf(`{"id":4,"cmd":"forward","params":{"from_thread_id":%q,"message_id":%q,"to_thread_ids":[%q]}}`, coloc, sent.MessageID, peer),
		fmt.Sprintf(`{"id":5,"cmd":"send_voice","params":{"thread_id":%q,"path":%q,"duration_ms":1000,"client_id":"v1"}}`, peer, wav),
		fmt.Sprintf(`{"id":6,"cmd":"create_poll","params":{"thread_id":%q,"question":"Ciné ?","options":["Oui","Non"]}}`, coloc),
		fmt.Sprintf(`{"id":7,"cmd":"mute","params":{"thread_id":%q,"until_ms":-1}}`, coloc),
		fmt.Sprintf(`{"id":8,"cmd":"rename_thread","params":{"thread_id":%q,"name":"Coloc 2"}}`, coloc),
		fmt.Sprintf(`{"id":9,"cmd":"set_thread_image","params":{"thread_id":%q,"path":%q}}`, coloc, img),
		fmt.Sprintf(`{"id":10,"cmd":"add_participants","params":{"thread_id":%q,"user_ids":[%q]}}`, coloc, demoPeople[9].ID),
		fmt.Sprintf(`{"id":11,"cmd":"set_admin","params":{"thread_id":%q,"user_id":%q,"admin":true}}`, coloc, demoPeople[9].ID),
		fmt.Sprintf(`{"id":12,"cmd":"remove_participant","params":{"thread_id":%q,"user_id":%q}}`, coloc, demoPeople[9].ID),
		fmt.Sprintf(`{"id":13,"cmd":"block_user","params":{"user_id":%q,"blocked":true}}`, peer),
		`{"id":14,"cmd":"search_users","params":{"query":"zo"}}`,
		fmt.Sprintf(`{"id":15,"cmd":"open_thread","params":{"user_id":%q}}`, demoStrangers[1].ID),
		fmt.Sprintf(`{"id":16,"cmd":"create_group","params":{"user_ids":[%q,%q],"name":"Projet"}}`, demoPeople[2].ID, demoPeople[3].ID),
		fmt.Sprintf(`{"id":17,"cmd":"accept_request","params":{"thread_id":%q}}`, demoStrangers[0].ID),
		fmt.Sprintf(`{"id":18,"cmd":"unsend","params":{"thread_id":%q,"message_id":%q}}`, coloc, sent.MessageID),
		fmt.Sprintf(`{"id":19,"cmd":"delete_for_me","params":{"thread_id":%q,"message_id":%q}}`, coloc, sent.MessageID),
		fmt.Sprintf(`{"id":20,"cmd":"leave_thread","params":{"thread_id":%q}}`, coloc),
		fmt.Sprintf(`{"id":21,"cmd":"delete_thread","params":{"thread_id":%q}}`, demoStrangers[1].ID),
		fmt.Sprintf(`{"id":22,"cmd":"create_poll","params":{"thread_id":%q,"question":"Q","options":["a","b"]}}`, peer),
	}
	for _, step := range steps {
		h.send(step)
	}
	for id := int64(3); id <= 21; id++ {
		h.okResult(id, nil)
	}
	if h.errCode(22) != CodeUnsupported {
		t.Fatal("polls in 1:1 must be unsupported")
	}
	var fwd ForwardResult
	h.okResult(4, &fwd)
	var users []UserResult
	h.okResult(14, &users)
	var opened, created Thread
	h.okResult(15, &opened)
	h.okResult(16, &created)
	if len(fwd.MessageIDs) != 1 || fwd.MessageIDs[0] == "" || len(users) == 0 || users[0].Name != "Zoé Fontaine" || opened.ID != demoStrangers[1].ID || !created.IsGroup || created.Name != "Projet" || !created.IsAdmin {
		t.Fatalf("unexpected results %v %+v %+v %+v", fwd, users, opened, created)
	}
	var voiceEcho, forwardEcho, pollMsg bool
	for _, raw := range h.events(EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		voiceEcho = voiceEcho || m.ClientID == "v1" && len(m.Attachments) == 1 && m.Attachments[0].Voice
		forwardEcho = forwardEcho || m.ID == fwd.MessageIDs[0] && m.Forwarded
		pollMsg = pollMsg || m.Poll != nil && m.Poll.Question == "Ciné ?"
	}
	if !voiceEcho || !forwardEcho || !pollMsg {
		t.Fatalf("missing echoes voice=%v forward=%v poll=%v", voiceEcho, forwardEcho, pollMsg)
	}
	removed := h.events(EventMessageRemoved)
	if len(removed) != 1 || !strings.Contains(string(removed[0]), sent.MessageID) {
		t.Fatalf("delete_for_me must emit message_removed: %s", removed)
	}
	if len(h.events(EventThreadRemoved)) != 1 {
		t.Fatal("delete_thread must emit thread_removed")
	}
	view, _ := h.demo.store.ThreadView(coloc)
	if view.Name != "Coloc 2" || view.MutedUntilMS == nil || *view.MutedUntilMS != -1 || view.CanManage {
		t.Fatalf("group changes not applied: %+v", view)
	}
	if v, _ := h.demo.store.ThreadView(peer); !v.Blocked {
		t.Fatal("block not applied")
	}
	if v, _ := h.demo.store.ThreadView(demoStrangers[0].ID); v.Folder != FolderInbox {
		t.Fatal("accept_request not applied")
	}
	h.send(fmt.Sprintf(`{"id":30,"cmd":"rename_thread","params":{"thread_id":%q,"name":"x"}}`, coloc))
	if h.errCode(30) != CodeUnsupported {
		t.Fatal("after leaving, group management must be unsupported")
	}
	var updated bool
	for _, raw := range h.events(EventMessageUpdate) {
		updated = updated || strings.Contains(string(raw), `"edited":true`) && strings.Contains(string(raw), "corrigé")
	}
	if !updated {
		t.Fatal("edit_message must emit message_updated")
	}
}

func TestDemoRequestThreadAndVoice(t *testing.T) {
	h := newDemoHarness(t)
	stranger := demoStrangers[0].ID
	h.demo.Messages(context.Background(), stranger, 0, 50, true)
	time.Sleep(demoBackfillDelay + 300*time.Millisecond)
	msgs := h.demo.store.Messages(stranger, 0, 0)
	if len(msgs) != 1 {
		t.Fatalf("the request thread must not grow fake history, got %d messages", len(msgs))
	}
	for _, m := range msgs {
		if m.IsOutgoing || m.SenderID != stranger {
			t.Fatalf("a message request must only contain the stranger's messages: %+v", m)
		}
	}
	path, _, duration, err := demoVoiceFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	frames, rate, samples, err := walkMP3Frames(data)
	if err != nil || rate != 24000 || int64(frames*samples)*1000/int64(rate) < duration {
		t.Fatalf("demo voice file must be a well-formed 24 kHz mp3: %v frames=%d rate=%d", err, frames, rate)
	}
}

func TestDemoReceiptsMediaAndSearch(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	h.send(`{"id":2,"cmd":"threads"}`)
	var threads []Thread
	h.okResult(2, &threads)
	withReceipts := 0
	for _, th := range threads {
		if len(th.ReadReceipts) > 0 {
			withReceipts++
		}
	}
	if withReceipts == 0 {
		t.Fatal("demo needs read receipts on some threads")
	}
	threadID := threads[0].ID

	dir := t.TempDir()
	pngPath := filepath.Join(dir, "photo.png")
	f, _ := os.Create(pngPath)
	png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 64, 48)))
	f.Close()
	h.send(fmt.Sprintf(`{"id":3,"cmd":"send_media","params":{"thread_id":%q,"paths":[%q,%q],"caption":"vacances"}}`, threadID, pngPath, pngPath))
	var sent SendMediaResult
	h.okResult(3, &sent)
	if len(sent.MessageIDs) != 2 {
		t.Fatalf("unexpected send_media result %+v", sent)
	}
	var first Message
	for _, raw := range h.events(EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		if m.ID == sent.MessageIDs[0] {
			first = m
		}
	}
	if first.Text != "vacances" || len(first.Attachments) != 1 || first.Attachments[0].Kind != "image" || first.Attachments[0].Width != 64 {
		t.Fatalf("bad echo %+v", first)
	}
	h.send(fmt.Sprintf(`{"id":4,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q}}`, threadID, first.ID, first.Attachments[0].ID))
	h.okResult(4, nil)
	h.send(fmt.Sprintf(`{"id":5,"cmd":"send_media","params":{"thread_id":%q,"paths":["relative.png"]}}`, threadID))
	if code := h.errCode(5); code != CodeBadRequest {
		t.Fatalf("got %s", code)
	}

	h.send(fmt.Sprintf(`{"id":6,"cmd":"react","params":{"thread_id":%q,"message_id":%q,"emoji":"🔥"}}`, threadID, first.ID))
	h.okResult(6, nil)
	updates := h.events(EventMessageUpdate)
	if len(updates) == 0 || !strings.Contains(string(updates[len(updates)-1]), `"emoji":"🔥","count":1,"mine":true`) {
		t.Fatalf("react must come back as message_updated: %s", updates)
	}
	h.send(fmt.Sprintf(`{"id":7,"cmd":"react","params":{"thread_id":%q,"message_id":%q,"emoji":""}}`, threadID, first.ID))
	h.okResult(7, nil)
	h.send(fmt.Sprintf(`{"id":8,"cmd":"set_typing","params":{"thread_id":%q,"typing":true}}`, threadID), `{"id":9,"cmd":"search","params":{"query":"VACANCES"}}`, `{"id":10,"cmd":"react","params":{"thread_id":"x","message_id":"y"}}`)
	h.okResult(8, nil)
	var hits []SearchHit
	h.okResult(9, &hits)
	if len(hits) == 0 || hits[0].Message.ID != first.ID {
		t.Fatalf("search must find the caption: %+v", hits)
	}
	if code := h.errCode(10); code != CodeBadRequest {
		t.Fatalf("missing emoji must be bad_request, got %s", code)
	}

	media := h.demo.mediaThreadID()
	var all []MediaItem
	before := int64(0)
	for id := 100; id < 140; id++ {
		params := fmt.Sprintf(`{"thread_id":%q,"limit":60}`, media)
		if before > 0 {
			params = fmt.Sprintf(`{"thread_id":%q,"limit":60,"before_ms":%d}`, media, before)
		}
		h.send(fmt.Sprintf(`{"id":%d,"cmd":"media_history","params":%s}`, id, params))
		var res MediaHistoryResult
		h.okResult(int64(id), &res)
		all = append(all, res.Items...)
		if !res.HasMore || len(res.Items) == 0 {
			break
		}
		before = res.Items[len(res.Items)-1].TimestampMS
	}
	if len(all) < 200 {
		t.Fatalf("demo media history too short: %d", len(all))
	}
	span := time.Duration(all[0].TimestampMS-all[len(all)-1].TimestampMS) * time.Millisecond
	if span < 60*24*time.Hour {
		t.Fatalf("demo media history must span months, got %s", span)
	}
	for i := 1; i < len(all); i++ {
		if all[i].TimestampMS > all[i-1].TimestampMS {
			t.Fatal("media history must be newest first across pages")
		}
	}
}

func TestClientIDsOnDemoSendMedia(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	dir := t.TempDir()
	a, c := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
	os.WriteFile(a, encodedImage(t, 8, 8, false), 0o600)
	os.WriteFile(c, encodedImage(t, 8, 8, false), 0o600)
	thread := demoPeople[0].ID
	h.send(
		fmt.Sprintf(`{"id":2,"cmd":"send_media","params":{"thread_id":%q,"paths":[%q,%q],"client_ids":["x1","x2"]}}`, thread, a, c),
		fmt.Sprintf(`{"id":3,"cmd":"send_media","params":{"thread_id":%q,"paths":[%q,%q],"client_ids":["only-one"]}}`, thread, a, c),
	)
	var res SendMediaResult
	h.okResult(2, &res)
	if h.errCode(3) != CodeBadRequest {
		t.Fatal("client_ids must have one entry per path")
	}
	echoes := map[string]string{}
	for _, raw := range h.events(EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		echoes[m.ID] = m.ClientID
	}
	if len(res.MessageIDs) != 2 || echoes[res.MessageIDs[0]] != "x1" || echoes[res.MessageIDs[1]] != "x2" {
		t.Fatalf("client_ids not echoed per item: %v %v", res.MessageIDs, echoes)
	}
}

func TestDemoAvatar(t *testing.T) {
	h := newDemoHarness(t)
	var threads []Thread
	h.send(`{"id":1,"cmd":"threads"}`)
	h.okResult(1, &threads)
	var avatarURL string
	for _, th := range threads {
		if !th.IsGroup && th.AvatarURL != "" {
			avatarURL = th.AvatarURL
			break
		}
	}
	if !strings.HasPrefix(avatarURL, "https://"+demoAvatarHost+"/") {
		t.Fatalf("demo contacts must carry avatar URLs, got %q", avatarURL)
	}
	h.send(fmt.Sprintf(`{"id":2,"cmd":"avatar","params":{"url":%q}}`, avatarURL))
	var res AvatarResult
	h.okResult(2, &res)
	f, err := os.Open(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != demoAvatarSide {
		t.Fatalf("demo avatar is not a generated PNG: %v %+v", err, cfg)
	}
}

func TestDemoThumbAndMediaHistoryMaxPages(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	thread := h.demo.mediaThreadID()
	before := h.demo.generated[thread]
	h.send(fmt.Sprintf(`{"id":2,"cmd":"media_history","params":{"thread_id":%q,"limit":60,"max_pages":0}}`, thread))
	var res MediaHistoryResult
	h.okResult(2, &res)
	h.demo.mu.Lock()
	after := h.demo.generated[thread]
	h.demo.mu.Unlock()
	if after != before || !res.HasMore {
		t.Fatalf("max_pages 0 must answer from the store only: generated %d -> %d, %+v", before, after, res)
	}
	h.send(fmt.Sprintf(`{"id":3,"cmd":"media_history","params":{"thread_id":%q,"limit":60,"max_pages":-1}}`, thread))
	if h.errCode(3) != CodeBadRequest {
		t.Fatal("negative max_pages must be rejected")
	}
	h.send(fmt.Sprintf(`{"id":4,"cmd":"media_history","params":{"thread_id":%q,"limit":5}}`, thread))
	h.okResult(4, &res)
	if len(res.Items) == 0 {
		t.Fatal("default max_pages must fetch")
	}
	item := res.Items[0]
	h.send(
		fmt.Sprintf(`{"id":5,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q,"variant":"thumb"}}`, thread, item.MessageID, item.Attachment.ID),
		fmt.Sprintf(`{"id":6,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q}}`, thread, item.MessageID, item.Attachment.ID),
		fmt.Sprintf(`{"id":7,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q,"variant":"huge"}}`, thread, item.MessageID, item.Attachment.ID),
	)
	var thumb, full MediaResult
	h.okResult(5, &thumb)
	h.okResult(6, &full)
	if h.errCode(7) != CodeBadRequest {
		t.Fatal("unknown variant must be rejected")
	}
	thumbInfo, _ := os.Stat(thumb.Path)
	fullInfo, _ := os.Stat(full.Path)
	if thumb.Width == 0 || max(thumb.Width, thumb.Height) > demoThumbSide || thumbInfo == nil || fullInfo == nil || thumbInfo.Size() >= fullInfo.Size() {
		t.Fatalf("demo thumb must be a smaller image: %+v %+v", thumb, full)
	}
}
