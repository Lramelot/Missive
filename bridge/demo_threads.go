package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	demoThemeID      = 900001
	demoGroupKeyBase = 7_100_000_000_000_000
)

var demoStrangers = []demoPerson{
	{"100004781234601", "Zoé Fontaine", "Zoé"},
	{"100004781234602", "Maxime Girard", "Maxime"},
	{"100004781234603", "Clara Bonnet", "Clara"},
}

func demoGroupID(spec int) string {
	return strconv.FormatInt(7_000_000_000_000_000+int64(spec)*7919, 10)
}

func (d *DemoBackend) demoThreadID(spec int) string {
	s := demoThreadSpecs[spec]
	if len(s.Members) > 1 || s.Name != "" {
		return demoGroupID(spec)
	}
	return demoPeople[s.Members[0]].ID
}

func (d *DemoBackend) demoMessage(threadID string, e2ee bool, ts int64, sender string) Message {
	msg := Message{
		ID:          d.nextMessageID(e2ee),
		ThreadID:    threadID,
		SenderID:    sender,
		TimestampMS: ts,
		IsOutgoing:  sender == d.user.ID,
		Attachments: []Attachment{},
	}
	if msg.IsOutgoing {
		msg.SenderName = d.user.Name
	} else if sender != "" {
		msg.SenderName = d.store.ContactName(sender)
	}
	return msg
}

func (d *DemoBackend) systemLine(threadID string, e2ee bool, ts int64, text string) Message {
	msg := d.demoMessage(threadID, e2ee, ts, "")
	msg.System = true
	msg.Text = text
	return msg
}

func (d *DemoBackend) latestTS(threadID string) int64 {
	if rec, ok := d.store.Thread(threadID); ok {
		return rec.LastActivityMS
	}
	return time.Now().UnixMilli()
}

func (d *DemoBackend) populateThreadSettings() {
	now := time.Now().UnixMilli()
	self := d.user.ID
	coloc := d.demoThreadID(1)
	annecy := d.demoThreadID(4)
	camille := d.demoThreadID(0)
	famille := d.demoThreadID(7)
	nathan := d.demoThreadID(3)

	for spec := range demoThreadSpecs {
		id := d.demoThreadID(spec)
		members := d.members[id]
		d.store.UpdateThread(id, func(rec *threadRec) {
			if !rec.IsGroup || len(members) == 0 {
				return
			}
			if spec == 1 || spec == 7 || spec == 13 {
				setAdmin(rec, self, true)
			}
			setAdmin(rec, members[0], true)
		})
		if len(members) > 0 {
			d.store.SetDeliveryReceipt(id, members[0], d.latestTS(id))
		}
	}

	d.store.UpdateThread(coloc, func(rec *threadRec) {
		rec.ThemeColor = "#7B61FF"
		rec.ThemeID = demoThemeID
		rec.QuickEmoji = "🍕"
	})
	for i, color := range []int64{0xFF7B61FF, 0xFFE05AA8, 0xFFFF8A3D} {
		d.store.SetThemeGradient(demoThemeID, int64(i), color)
	}
	d.store.UpdateThread(d.demoThreadID(5), func(rec *threadRec) { rec.MuteExpireMS = now + 3*3_600_000 })
	d.store.UpdateThread(d.demoThreadID(12), func(rec *threadRec) { rec.Folder = FolderArchived })
	d.store.UpdateThread(d.demoThreadID(9), func(rec *threadRec) { rec.EphemeralTTLMS = 24 * 3_600_000 })
	d.store.UpdateThread(d.demoThreadID(8), func(rec *threadRec) { rec.Blocked = true })

	colocTS := d.latestTS(coloc)
	put := func(msg Message) Message {
		d.store.PutMessage(msg)
		return msg
	}
	put(d.systemLine(coloc, false, colocTS-9*3_600_000, "Hugo a renommé le groupe « Coloc 🏠 »"))
	put(d.systemLine(coloc, false, colocTS-8*3_600_000, "Léa a ajouté Lucas"))
	poll := d.demoMessage(coloc, false, colocTS-5*3_600_000, demoPeople[2].ID)
	put(poll)
	pollID := "poll_" + strconv.FormatInt(now%1_000_000, 10)
	d.store.UpsertPoll(pollID, coloc, poll.ID, "Pizza ou raclette jeudi ?", []pollOptionRec{{ID: "opt_1", Text: "Pizza 🍕"}, {ID: "opt_2", Text: "Raclette 🧀"}, {ID: "opt_3", Text: "Les deux"}}, true)
	d.store.SetPollVotes(pollID, demoPeople[1].ID, []string{"opt_1"}, poll.TimestampMS+60_000)
	d.store.SetPollVotes(pollID, demoPeople[3].ID, []string{"opt_2", "opt_3"}, poll.TimestampMS+120_000)
	d.store.SetPollVotes(pollID, self, []string{"opt_1"}, poll.TimestampMS+180_000)
	pinned := d.store.Messages(coloc, 0, 0)
	if len(pinned) > 3 {
		d.store.UpdateThread(coloc, func(rec *threadRec) {
			setPin(rec, pinned[1].ID, colocTS-2*3_600_000)
			setPin(rec, poll.ID, colocTS-3_600_000)
		})
	}

	annecyTS := d.latestTS(annecy)
	location := d.demoMessage(annecy, false, annecyTS-4*3_600_000, demoPeople[6].ID)
	lat, long := 45.8992, 6.1294
	location.Attachments = []Attachment{{ID: "location_0", Kind: "location", Title: "Lac d'Annecy", Description: "Quai Napoléon III, 74000 Annecy", URL: mapsLink(&lat, &long, ""), Latitude: &lat, Longitude: &long}}
	put(location)
	forwarded := d.demoMessage(annecy, false, annecyTS-3*3_600_000, demoPeople[7].ID)
	forwarded.Text = "Le programme du samedi : rando le matin, baignade l'après-midi."
	forwarded.Forwarded = true
	put(forwarded)
	put(d.systemLine(annecy, false, annecyTS-2*3_600_000, "Appel vidéo · 12 min"))

	camilleTS := d.latestTS(camille)
	story := d.demoMessage(camille, true, camilleTS-6*3_600_000, demoPeople[0].ID)
	story.Text = "Trop belle cette vue 😍"
	story.Attachments = []Attachment{{ID: "story_0", Kind: "story", Title: "A répondu à votre story"}}
	put(story)
	put(d.systemLine(camille, true, camilleTS-5*3_600_000, "Appel manqué"))
	if path, waveform, duration, err := demoVoiceFile(d.cacheDir); err == nil {
		voice := d.demoMessage(camille, true, camilleTS-4*3_600_000, demoPeople[0].ID)
		voice.Attachments = []Attachment{{ID: "voice_0", Kind: "audio", MimeType: voiceMime, FileName: "audioclip.mp3", DurationMS: duration, Voice: true, Waveform: waveform}}
		put(voice)
		d.store.SetMediaPath(voice.ID, "voice_0", path, voiceMime)
	}

	contact := d.demoMessage(nathan, true, d.latestTS(nathan)-3_600_000, demoPeople[5].ID)
	contact.Attachments = []Attachment{{ID: "contact_0", Kind: "contact", Title: "Inès Leroy", Description: "+33 6 12 34 56 78"}}
	put(contact)
	put(d.systemLine(famille, true, d.latestTS(famille)-30*60_000, "Sarah a défini la réaction rapide sur ❤️"))

	stranger := demoStrangers[0]
	d.store.SetContact(stranger.ID, stranger.Name, stranger.FirstName, demoAvatarURL(stranger.ID))
	d.members[stranger.ID] = []string{stranger.ID}
	d.store.UpdateThread(stranger.ID, func(rec *threadRec) {
		rec.PeerID = stranger.ID
		rec.ThreadType = 1
		rec.Folder = FolderRequests
		rec.Members = map[string]bool{self: true, stranger.ID: true}
		rec.UnreadCount = 1
	})
	request := d.demoMessage(stranger.ID, false, now-25*60_000, stranger.ID)
	request.Text = "Bonjour ! Je crois qu'on s'est croisés au salon de la photo, je vous écris pour l'expo 📸"
	put(request)
	d.store.UpdateThread(stranger.ID, func(rec *threadRec) {
		rec.LastActivityMS = request.TimestampMS
		rec.Snippet = request.Text
	})
}

func (d *DemoBackend) emitThreadIDs(ids ...string) {
	if views := d.store.ThreadViews(ids); len(views) > 0 {
		d.out.Emit(EventThreads, ThreadsEvent{Threads: views})
	}
}

func (d *DemoBackend) appendLive(msg Message) {
	d.store.PutMessage(msg)
	d.store.UpdateThread(msg.ThreadID, func(rec *threadRec) {
		rec.LastActivityMS = max(rec.LastActivityMS, msg.TimestampMS)
		rec.Snippet = snippetFor(msg)
	})
}

func (d *DemoBackend) emitNew(msgs ...Message) {
	threads := map[string]bool{}
	for _, msg := range msgs {
		d.out.Emit(EventMessage, d.store.Decorate(msg))
		threads[msg.ThreadID] = true
	}
	ids := make([]string, 0, len(threads))
	for id := range threads {
		ids = append(ids, id)
	}
	d.emitThreadIDs(ids...)
}

func (d *DemoBackend) demoGroup(threadID string, needManage bool) (threadRec, error) {
	rec, err := d.requireThread(threadID)
	if err != nil {
		return rec, err
	}
	if !rec.IsGroup || rec.E2EE {
		return rec, newError(CodeUnsupported, "only non-encrypted group conversations support this")
	}
	if needManage && !canManage(&rec, d.user.ID) {
		return rec, newError(CodeUnsupported, "you cannot manage this conversation")
	}
	return rec, nil
}

func (d *DemoBackend) SearchUsers(ctx context.Context, query string, limit int) ([]UserResult, error) {
	d.mu.Lock()
	err := d.requireLogin()
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	results := d.store.SearchContacts(query, limit)
	needle := foldText(query)
	var extra []UserResult
	for _, p := range demoStrangers {
		if strings.Contains(foldText(p.Name), needle) {
			extra = append(extra, UserResult{ID: p.ID, Name: p.Name, AvatarURL: demoAvatarURL(p.ID), ThreadID: d.store.PeerThreadID(p.ID)})
		}
	}
	return mergeSearchResults(results, extra, limit), nil
}

func (d *DemoBackend) knownPerson(userID string) (string, bool) {
	if name := d.store.ContactName(userID); name != "" {
		return name, true
	}
	for _, p := range demoStrangers {
		if p.ID == userID {
			d.store.SetContact(p.ID, p.Name, p.FirstName, demoAvatarURL(p.ID))
			return p.Name, true
		}
	}
	return "", false
}

func (d *DemoBackend) OpenThread(ctx context.Context, userID string) (Thread, error) {
	d.mu.Lock()
	if err := d.requireLogin(); err != nil {
		d.mu.Unlock()
		return Thread{}, err
	}
	if userID == d.user.ID {
		d.mu.Unlock()
		return Thread{}, newError(CodeBadRequest, "cannot open a conversation with yourself")
	}
	id := d.store.PeerThreadID(userID)
	if id == "" {
		if _, ok := d.knownPerson(userID); !ok {
			d.mu.Unlock()
			return Thread{}, newError(CodeNotFound, "unknown user %s", userID)
		}
		id = userID
		d.members[id] = []string{userID}
		d.store.UpdateThread(id, func(rec *threadRec) {
			rec.PeerID = userID
			rec.ThreadType = 1
			rec.Members = map[string]bool{d.user.ID: true, userID: true}
			rec.LastActivityMS = time.Now().UnixMilli()
		})
	}
	d.mu.Unlock()
	d.emitThreadIDs(id)
	view, _ := d.store.ThreadView(id)
	return view, nil
}

func (d *DemoBackend) CreateGroup(ctx context.Context, userIDs []string, name string) (Thread, error) {
	d.mu.Lock()
	if err := d.requireLogin(); err != nil {
		d.mu.Unlock()
		return Thread{}, err
	}
	for _, u := range userIDs {
		if _, ok := d.knownPerson(u); !ok {
			d.mu.Unlock()
			return Thread{}, newError(CodeNotFound, "unknown user %s", u)
		}
	}
	d.counter++
	id := strconv.FormatInt(demoGroupKeyBase+d.counter, 10)
	d.members[id] = slices.Clone(userIDs)
	now := time.Now().UnixMilli()
	d.store.UpdateThread(id, func(rec *threadRec) {
		rec.IsGroup = true
		rec.ThreadType = 2
		rec.Name = name
		rec.Members = map[string]bool{d.user.ID: true}
		for _, u := range userIDs {
			rec.Members[u] = true
		}
		setAdmin(rec, d.user.ID, true)
		rec.LastActivityMS = now
	})
	line := d.systemLine(id, false, now, "Vous avez créé le groupe")
	d.appendLive(line)
	d.mu.Unlock()
	d.emitNew(line)
	view, _ := d.store.ThreadView(id)
	return view, nil
}

func (d *DemoBackend) updateDemoThread(threadID string, fn func(rec *threadRec)) error {
	d.mu.Lock()
	_, err := d.requireThread(threadID)
	if err == nil {
		d.store.UpdateThread(threadID, fn)
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	d.emitThreadIDs(threadID)
	return nil
}

func (d *DemoBackend) AcceptRequest(ctx context.Context, threadID string) error {
	return d.updateDemoThread(threadID, func(rec *threadRec) { rec.Folder = FolderInbox })
}

func (d *DemoBackend) DeleteThread(ctx context.Context, threadID string) error {
	d.mu.Lock()
	_, err := d.requireThread(threadID)
	removed := err == nil && d.store.RemoveThread(threadID)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if removed {
		d.out.Emit(EventThreadRemoved, ThreadRemovedEvent{ThreadID: threadID})
	}
	return nil
}

func (d *DemoBackend) Mute(ctx context.Context, threadID string, untilMS int64) error {
	return d.updateDemoThread(threadID, func(rec *threadRec) { rec.MuteExpireMS = untilMS })
}

func (d *DemoBackend) groupChange(threadID string, needAdmin bool, apply func(rec *threadRec) (string, error)) error {
	d.mu.Lock()
	rec, err := d.demoGroup(threadID, true)
	if err == nil && needAdmin && !rec.Admins[d.user.ID] {
		err = newError(CodeUnsupported, "only group admins can change admins")
	}
	var line Message
	if err == nil {
		var text string
		var applyErr error
		d.store.UpdateThread(threadID, func(r *threadRec) { text, applyErr = apply(r) })
		err = applyErr
		if err == nil && text != "" {
			line = d.systemLine(threadID, false, time.Now().UnixMilli(), text)
			d.appendLive(line)
		}
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if line.ID != "" {
		d.emitNew(line)
	} else {
		d.emitThreadIDs(threadID)
	}
	return nil
}

func (d *DemoBackend) RenameThread(ctx context.Context, threadID, name string) error {
	return d.groupChange(threadID, false, func(rec *threadRec) (string, error) {
		rec.Name = name
		if name == "" {
			return "Vous avez retiré le nom du groupe", nil
		}
		return fmt.Sprintf("Vous avez renommé le groupe « %s »", name), nil
	})
}

func (d *DemoBackend) SetThreadImage(ctx context.Context, threadID, path string) error {
	if _, _, err := readThreadImage(path); err != nil {
		return err
	}
	return d.groupChange(threadID, false, func(rec *threadRec) (string, error) {
		rec.AvatarURL = demoAvatarURL("group-" + rec.ID + "-" + strconv.FormatInt(time.Now().UnixMilli(), 10))
		return "Vous avez changé la photo du groupe", nil
	})
}

func (d *DemoBackend) AddParticipants(ctx context.Context, threadID string, userIDs []string) error {
	names := make([]string, 0, len(userIDs))
	for _, u := range userIDs {
		name, ok := d.knownPerson(u)
		if !ok {
			return newError(CodeNotFound, "unknown user %s", u)
		}
		names = append(names, name)
	}
	return d.groupChange(threadID, false, func(rec *threadRec) (string, error) {
		for _, u := range userIDs {
			rec.Members[u] = true
			if !slices.Contains(d.members[rec.ID], u) {
				d.members[rec.ID] = append(d.members[rec.ID], u)
			}
		}
		return "Vous avez ajouté " + strings.Join(names, ", "), nil
	})
}

func (d *DemoBackend) RemoveParticipant(ctx context.Context, threadID, userID string) error {
	if userID == d.user.ID {
		return d.LeaveThread(ctx, threadID)
	}
	name := d.store.ContactName(userID)
	return d.groupChange(threadID, false, func(rec *threadRec) (string, error) {
		if !rec.Members[userID] {
			return "", newError(CodeBadRequest, "user %s is not a member of this group", userID)
		}
		delete(rec.Members, userID)
		delete(rec.Admins, userID)
		d.members[rec.ID] = slices.DeleteFunc(slices.Clone(d.members[rec.ID]), func(m string) bool { return m == userID })
		return fmt.Sprintf("Vous avez retiré %s du groupe", name), nil
	})
}

func (d *DemoBackend) LeaveThread(ctx context.Context, threadID string) error {
	return d.groupChange(threadID, false, func(rec *threadRec) (string, error) {
		delete(rec.Members, d.user.ID)
		delete(rec.Admins, d.user.ID)
		return "Vous avez quitté le groupe", nil
	})
}

func (d *DemoBackend) SetAdmin(ctx context.Context, threadID, userID string, admin bool) error {
	name := d.store.ContactName(userID)
	return d.groupChange(threadID, true, func(rec *threadRec) (string, error) {
		if !rec.Members[userID] {
			return "", newError(CodeBadRequest, "user %s is not a member of this group", userID)
		}
		if !setAdmin(rec, userID, admin) {
			return "", nil
		}
		if admin {
			return fmt.Sprintf("Vous avez nommé %s administrateur", name), nil
		}
		return fmt.Sprintf("Vous avez retiré les droits d'administrateur de %s", name), nil
	})
}

func (d *DemoBackend) BlockUser(ctx context.Context, userID string, blocked bool) error {
	d.mu.Lock()
	err := d.requireLogin()
	id := ""
	if err == nil {
		if id = d.store.PeerThreadID(userID); id != "" {
			d.store.UpdateThread(id, func(rec *threadRec) { rec.Blocked = blocked })
		} else if _, ok := d.knownPerson(userID); !ok {
			err = newError(CodeNotFound, "unknown user %s", userID)
		}
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if id != "" {
		d.emitThreadIDs(id)
	}
	return nil
}
