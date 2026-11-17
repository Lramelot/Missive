package main

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type demoPerson struct {
	ID        string
	Name      string
	FirstName string
}

var demoNoPicture = map[int]bool{5: true, 12: true}

var demoPeople = []demoPerson{
	{"100004781234501", "Camille Martin", "Camille"},
	{"100004781234502", "Hugo Bernard", "Hugo"},
	{"100004781234503", "Léa Dubois", "Léa"},
	{"100004781234504", "Lucas Thomas", "Lucas"},
	{"100004781234505", "Chloé Robert", "Chloé"},
	{"100004781234506", "Nathan Richard", "Nathan"},
	{"100004781234507", "Manon Petit", "Manon"},
	{"100004781234508", "Théo Durand", "Théo"},
	{"100004781234509", "Inès Leroy", "Inès"},
	{"100004781234510", "Louis Moreau", "Louis"},
	{"100004781234511", "Emma Simon", "Emma"},
	{"100004781234512", "Jules Laurent", "Jules"},
	{"100004781234513", "Sarah Lefebvre", "Sarah"},
	{"100004781234514", "Gabriel Michel", "Gabriel"},
	{"100004781234515", "Alice Garcia", "Alice"},
}

var demoLines = []string{
	"Salut ! Ça va ?",
	"Tu es dispo ce soir ?",
	"Je regarde ça et je te redis",
	"Haha trop bien 😂",
	"On se retrouve à 19h devant le ciné ?",
	"J'ai oublié mes clés chez toi je crois",
	"Tu as vu le dernier épisode ?",
	"Pas de spoil stp 🙏",
	"Ok parfait",
	"Je suis en retard de 10 min, désolé",
	"Merci beaucoup !",
	"Bonne idée, on fait comme ça",
	"Tu peux m'envoyer l'adresse ?",
	"C'est au 12 rue des Lilas, 3e étage",
	"Joyeux anniversaire !! 🎉",
	"Je pense qu'on devrait réserver avant vendredi",
	"Qui ramène quoi pour samedi ?",
	"Moi je prends les boissons",
	"Je m'occupe du dessert 🍰",
	"Il pleut encore ici...",
	"Tu rentres quand ?",
	"Demain matin normalement",
	"Envoie-moi les photos quand tu peux",
	"Grave, j'adore",
	"Attends je t'appelle",
	"Le train a 20 minutes de retard",
	"Ça marche, à tout à l'heure",
	"Tu as pensé à prendre les billets ?",
	"Oui c'est fait, je te les transfère",
	"Bonne nuit 😴",
	"On s'organise un resto la semaine prochaine ?",
	"Mardi ou jeudi pour moi",
	"Jeudi c'est parfait",
	"Je viens de finir le livre que tu m'as prêté, génial",
	"Tu me diras ce que tu en penses",
	"👍",
	"Trop hâte !",
	"Je suis dans le métro, je te réponds après",
	"Tu as le numéro du plombier ?",
	"Regarde ce que j'ai trouvé",
}

type demoThreadSpec struct {
	Name    string
	Members []int
	E2EE    bool
	Unread  int
	Muted   bool
}

var demoThreadSpecs = []demoThreadSpec{
	{Members: []int{0}, E2EE: true, Unread: 2},
	{Name: "Coloc 🏠", Members: []int{1, 2, 3}, Unread: 5},
	{Members: []int{4}, Muted: true},
	{Members: []int{5}, E2EE: true},
	{Name: "Week-end à Annecy", Members: []int{0, 6, 7, 8}, Unread: 1},
	{Members: []int{9}, Unread: 1},
	{Members: []int{10}, E2EE: true},
	{Name: "Famille", Members: []int{11, 12, 13}, E2EE: true},
	{Members: []int{11}},
	{Members: []int{12}},
	{Name: "Foot du jeudi ⚽", Members: []int{1, 3, 5, 7, 9, 13}, Muted: true},
	{Members: []int{13}, E2EE: true},
	{Members: []int{14}},
	{Name: "", Members: []int{2, 4, 14}},
	{Members: []int{6}},
}

const demoMaxHistory = 400

var demoImageSizes = [][2]int{{1280, 960}, {960, 1280}, {1024, 1024}, {1600, 900}, {640, 480}}

type demoLink struct {
	url, title, description, source string
}

var demoLinks = []demoLink{
	{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "Concert complet en plein air", "Une soirée d'été inoubliable", "youtube.com"},
	{"https://www.instagram.com/reel/C0demo1234/", "Reel Instagram", "Recette de crêpes en 30 secondes", "Instagram"},
	{"https://www.facebook.com/events/1234567890/", "Vide-grenier du quartier", "Samedi 9h – 17h, place du marché", "Facebook"},
	{"https://fr.wikipedia.org/wiki/Annecy", "Annecy — Wikipédia", "Commune française de Haute-Savoie", "fr.wikipedia.org"},
}

var demoReactionEmojis = []string{"❤️", "😂", "👍", "😮", "😢"}

func (d *DemoBackend) decorateDemo() {
	for _, th := range d.store.ListThreads(0) {
		members := d.members[th.ID]
		for _, msg := range d.store.Messages(th.ID, 0, 0) {
			if d.rng.IntN(7) != 0 || len(members) == 0 {
				continue
			}
			emoji := demoReactionEmojis[d.rng.IntN(len(demoReactionEmojis))]
			d.store.SetReaction(msg.ID, members[d.rng.IntN(len(members))], emoji, msg.TimestampMS+int64(1+d.rng.IntN(600))*1000)
			if d.rng.IntN(3) == 0 {
				d.store.SetReaction(msg.ID, d.user.ID, emoji, msg.TimestampMS+int64(601+d.rng.IntN(600))*1000)
			}
		}
	}
	d.store.SetNickname(demoPeople[0].ID, demoPeople[0].ID, "Cam 🌸")
	d.store.SetNickname(demoPeople[9].ID, demoPeople[9].ID, "Loulou")
	groupID := strconv.FormatInt(7_000_000_000_000_000+int64(1)*7919, 10)
	d.store.SetNickname(groupID, demoPeople[2].ID, "La boss")
}

type DemoBackend struct {
	out     Emitter
	log     zerolog.Logger
	store   *Store
	dataDir string

	mu        sync.Mutex
	rng       *rand.Rand
	loggedIn  bool
	connected bool
	user      User
	members   map[string][]string
	generated map[string]int
	stopLoop  context.CancelFunc
	counter   int64

	historyRestored bool
	pinAttempts     int
	active          bool

	avatars   *avatarCache
	backfills map[string]bool
	archive   archiveJobs
}

func NewDemoBackend(dataDir string, out Emitter, log zerolog.Logger) *DemoBackend {
	d := &DemoBackend{
		out:       out,
		log:       log,
		store:     NewStore(),
		dataDir:   dataDir,
		rng:       rand.New(rand.NewPCG(42, uint64(time.Now().UnixNano()))),
		loggedIn:  true,
		user:      User{ID: "100004781234500", Name: "Moi Démo"},
		members:   make(map[string][]string),
		generated: make(map[string]int),

		pinAttempts: demoPINAttempts,
		backfills:   make(map[string]bool),
	}
	d.avatars = newAvatarCache(dataDir, demoAvatarFetcher, nil)
	d.populate()
	return d
}

func (d *DemoBackend) nextMessageID(e2ee bool) string {
	d.counter++
	if e2ee {
		return fmt.Sprintf("3EB0%016X%04X", d.rng.Uint64(), d.counter&0xffff)
	}
	return fmt.Sprintf("mid.$demo%012d%06d", d.rng.Int64N(1_000_000_000_000), d.counter)
}

func (d *DemoBackend) populate() {
	now := time.Now().UnixMilli()
	d.store.SetSelfID(d.user.ID)
	d.store.SetSelfName(d.user.Name)
	for i, p := range demoPeople {
		avatar := demoAvatarURL(p.ID)
		if demoNoPicture[i] {
			avatar = ""
		}
		d.store.SetContact(p.ID, p.Name, p.FirstName, avatar)
		switch {
		case i%4 == 0:
			d.store.SetPresence(p.ID, true, now, 0)
		case i%4 == 1:
			d.store.SetPresence(p.ID, false, now-int64(3+d.rng.IntN(50))*60_000, 0)
		case i%4 == 2:
			d.store.SetPresence(p.ID, false, now-int64(2+d.rng.IntN(20))*3_600_000, 0)
		}
	}
	cursor := now - int64(d.rng.IntN(5))*60_000
	for i, spec := range demoThreadSpecs {
		isGroup := len(spec.Members) > 1 || spec.Name != ""
		var id, peer string
		memberIDs := make([]string, 0, len(spec.Members)+1)
		for _, idx := range spec.Members {
			memberIDs = append(memberIDs, demoPeople[idx].ID)
		}
		if isGroup {
			id = strconv.FormatInt(7_000_000_000_000_000+int64(i)*7919, 10)
		} else {
			peer = memberIDs[0]
			id = peer
		}
		d.members[id] = memberIDs
		threadType := int64(1)
		if isGroup {
			threadType = 2
		}
		if spec.E2EE {
			threadType += 14
		}
		lastActivity := cursor
		cursor -= int64(10+d.rng.IntN(600)) * 60_000 * int64(1+i/4)
		d.store.UpdateThread(id, func(rec *threadRec) {
			rec.Name = spec.Name
			rec.IsGroup = isGroup
			rec.E2EE = spec.E2EE
			rec.ThreadType = threadType
			rec.PeerID = peer
			rec.Members = map[string]bool{d.user.ID: true}
			for _, m := range memberIDs {
				rec.Members[m] = true
			}
		})
		count := 18 + d.rng.IntN(18)
		ts := lastActivity
		msgs := make([]Message, 0, count)
		for j := 0; j < count; j++ {
			outgoing := j >= spec.Unread && d.rng.IntN(3) == 0
			msgs = append(msgs, d.makeMessage(id, spec.E2EE, ts, outgoing))
			ts -= int64(1+d.rng.IntN(90)) * 60_000
			if d.rng.IntN(8) == 0 {
				ts -= int64(3+d.rng.IntN(20)) * 3_600_000
			}
		}
		if isGroup && spec.Unread > 0 {
			msgs[0].Deleted = false
			msgs[0].Edited = false
			msgs[0].Attachments = []Attachment{}
			msgs[0].Text = demoLines[d.rng.IntN(len(demoLines))]
			d.addMention(&msgs[0], d.user.ID)
		}
		for _, msg := range msgs {
			d.store.PutMessage(msg)
		}
		d.generated[id] = count
		last := msgs[0]
		d.store.UpdateThread(id, func(rec *threadRec) {
			rec.LastActivityMS = last.TimestampMS
			rec.Snippet = snippetFor(last)
			rec.UnreadCount = spec.Unread
			if spec.Muted {
				rec.MuteExpireMS = -1
			}
		})
	}
	d.decorateDemo()
	d.seedReceipts()
	d.populateV11()
}

func snippetFor(msg Message) string {
	if msg.Deleted {
		return ""
	}
	if msg.Text != "" {
		return msg.Text
	}
	if len(msg.Attachments) > 0 {
		switch msg.Attachments[0].Kind {
		case "image":
			return "📷 Photo"
		case "video":
			return "🎬 Video"
		case "audio":
			return "🎤 Audio"
		case "sticker":
			return "Sticker"
		case "link":
			return "🔗 " + firstNonEmpty(msg.Attachments[0].Title, msg.Attachments[0].URL)
		default:
			if title := msg.Attachments[0].Title; title != "" {
				return title
			}
			return "📎 Attachment"
		}
	}
	return ""
}

func (d *DemoBackend) makeMessage(threadID string, e2ee bool, ts int64, outgoing bool) Message {
	sender := d.user.ID
	senderName := d.user.Name
	if !outgoing {
		members := d.members[threadID]
		sender = members[d.rng.IntN(len(members))]
		senderName = d.store.ContactName(sender)
	}
	msg := Message{
		ID:          d.nextMessageID(e2ee),
		ThreadID:    threadID,
		SenderID:    sender,
		SenderName:  senderName,
		Text:        demoLines[d.rng.IntN(len(demoLines))],
		TimestampMS: ts,
		IsOutgoing:  outgoing,
		Attachments: []Attachment{},
	}
	switch roll := d.rng.IntN(40); {
	case roll <= 3:
		size := demoImageSizes[d.rng.IntN(len(demoImageSizes))]
		count := 1
		if roll == 3 {
			count = 2 + d.rng.IntN(3)
		}
		if roll != 2 {
			msg.Text = ""
		}
		msg.Attachments = make([]Attachment, 0, count)
		for i := 0; i < count; i++ {
			msg.Attachments = append(msg.Attachments, Attachment{
				ID:       "img_" + strconv.Itoa(i),
				Kind:     "image",
				FileName: "IMG_" + strconv.Itoa(1000+d.rng.IntN(9000)) + ".png",
				MimeType: "image/png",
				Width:    size[0],
				Height:   size[1],
				Size:     int64(size[0] * size[1] / 3),
			})
		}
	case roll == 4:
		msg.Attachments = []Attachment{{ID: "file_0", Kind: "file", FileName: "notes.txt", MimeType: "text/plain", Size: 120}}
	case roll == 5:
		msg.Text = ""
		msg.Attachments = []Attachment{{ID: "sticker_0", Kind: "sticker", MimeType: "image/png", Width: 240, Height: 240}}
	case roll == 6:
		msg.Edited = true
	case roll == 8 || roll == 9:
		link := demoLinks[d.rng.IntN(len(demoLinks))]
		if roll == 9 {
			msg.Text = ""
		}
		msg.Attachments = []Attachment{{
			ID:          "link_0",
			Kind:        "link",
			URL:         link.url,
			Title:       link.title,
			Description: link.description,
			Source:      link.source,
			Width:       1200,
			Height:      630,
		}}
	case roll == 7 && !outgoing:
		msg.Text = ""
		msg.Deleted = true
	}
	if members := d.members[threadID]; len(members) > 1 && !msg.Deleted && msg.Text != "" && len(msg.Attachments) == 0 && d.rng.IntN(6) == 0 {
		target := members[d.rng.IntN(len(members))]
		if !outgoing && (target == sender || d.rng.IntN(2) == 0) {
			target = d.user.ID
		}
		d.addMention(&msg, target)
	}
	return msg
}

func (d *DemoBackend) addMention(msg *Message, target string) {
	name := d.store.MemberName(msg.ThreadID, target)
	if target == d.user.ID {
		name = d.user.Name
	}
	if name == "" {
		return
	}
	prefix := "Coucou "
	msg.Mentions = []Mention{{Offset: len(prefix), Length: len(name), UserID: target}}
	msg.Text = prefix + name + " ! " + msg.Text
}

func (d *DemoBackend) requireLogin() error {
	if !d.loggedIn {
		return newError(CodeNotLoggedIn, "not logged in")
	}
	return nil
}

func (d *DemoBackend) Status(ctx context.Context) (StatusResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res := StatusResult{LoggedIn: d.loggedIn, Connected: d.connected}
	if d.loggedIn {
		user := d.user
		res.User = &user
	}
	return res, nil
}

func (d *DemoBackend) Login(ctx context.Context, cookies map[string]string) (User, error) {
	var missing []string
	for _, name := range []string{"xs", "c_user", "datr"} {
		if cookies[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return User{}, newError(CodeBadRequest, "missing cookies: %s", strings.Join(missing, ", "))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loggedIn {
		d.loggedIn = true
		d.store.Clear()
		d.members = make(map[string][]string)
		d.generated = make(map[string]int)
		d.populate()
	}
	return d.user, nil
}

func (d *DemoBackend) Logout(ctx context.Context) error {
	d.mu.Lock()
	wasConnected := d.connected
	d.loggedIn = false
	d.connected = false
	if d.stopLoop != nil {
		d.stopLoop()
		d.stopLoop = nil
	}
	d.mu.Unlock()
	_ = os.RemoveAll(filepath.Join(d.dataDir, avatarDirName))
	if wasConnected {
		d.out.Emit(EventConnection, ConnectionEvent{State: StateDisconnected, Detail: "logged out"})
	}
	return nil
}

func (d *DemoBackend) Connect(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.requireLogin(); err != nil {
		return err
	}
	if d.stopLoop != nil {
		return nil
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	d.stopLoop = cancel
	go d.connectLoop(loopCtx)
	return nil
}

func (d *DemoBackend) connectLoop(ctx context.Context) {
	d.out.Emit(EventConnection, ConnectionEvent{State: StateConnecting})
	if !sleepCtx(ctx, 400*time.Millisecond) {
		return
	}
	d.mu.Lock()
	d.connected = true
	user := d.user
	d.mu.Unlock()
	d.out.Emit(EventConnection, ConnectionEvent{State: StateConnected})
	d.out.Emit(EventThreads, ThreadsEvent{Threads: d.store.ListThreads(0)})
	d.out.Emit(EventReady, ReadyEvent{User: user})
	now := time.Now().UnixMilli()
	for _, peer := range []string{demoPeople[0].ID, demoPeople[4].ID} {
		d.store.SetPresence(peer, true, now, 0)
		d.out.Emit(EventPresence, PresenceEvent{UserID: peer, Online: true, LastSeenMS: now})
	}
	for {
		d.mu.Lock()
		wait := time.Duration(10_000+d.rng.IntN(10_001)) * time.Millisecond
		react := d.rng.IntN(demoReactionChanceOutOf) == 0
		d.mu.Unlock()
		if !sleepCtx(ctx, wait) {
			return
		}
		if react && d.simulateReaction() {
			continue
		}
		d.simulateIncoming(ctx)
	}
}

func sleepCtx(ctx context.Context, dur time.Duration) bool {
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (d *DemoBackend) simulateIncoming(ctx context.Context) {
	d.mu.Lock()
	threads := d.store.ListThreads(8)
	if len(threads) == 0 {
		d.mu.Unlock()
		return
	}
	thread := threads[d.rng.IntN(len(threads))]
	members := d.members[thread.ID]
	if len(members) == 0 {
		d.mu.Unlock()
		return
	}
	sender := members[d.rng.IntN(len(members))]
	typingFor := time.Duration(1500+d.rng.IntN(2000)) * time.Millisecond
	d.mu.Unlock()

	d.out.Emit(EventTyping, TypingEvent{ThreadID: thread.ID, UserID: sender, Typing: true})
	if !sleepCtx(ctx, typingFor) {
		return
	}
	d.mu.Lock()
	msg := d.makeMessage(thread.ID, thread.E2EE, time.Now().UnixMilli(), false)
	msg.SenderID = sender
	msg.SenderName = d.store.ContactName(sender)
	msg.Deleted = false
	msg.Edited = false
	if msg.Text == "" && len(msg.Attachments) == 0 {
		msg.Text = demoLines[d.rng.IntN(len(demoLines))]
	}
	d.store.PutMessage(msg)
	d.store.UpdateThread(thread.ID, func(rec *threadRec) {
		rec.LastActivityMS = msg.TimestampMS
		rec.Snippet = snippetFor(msg)
		rec.UnreadCount++
	})
	d.mu.Unlock()
	d.out.Emit(EventTyping, TypingEvent{ThreadID: thread.ID, UserID: sender, Typing: false})
	d.out.Emit(EventMessage, d.store.Decorate(msg))
	if view, ok := d.store.ThreadView(thread.ID); ok {
		d.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
}

func (d *DemoBackend) Threads(ctx context.Context, limit int) ([]Thread, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.requireLogin(); err != nil {
		return nil, err
	}
	return d.store.ListThreads(limit), nil
}

func (d *DemoBackend) Messages(ctx context.Context, threadID string, beforeMS int64, limit int, fetchMissing bool) ([]Message, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.requireLogin(); err != nil {
		return nil, err
	}
	rec, ok := d.store.Thread(threadID)
	if !ok {
		return nil, newError(CodeNotFound, "unknown thread %s", threadID)
	}
	msgs := d.store.Messages(threadID, beforeMS, limit)
	if fetchMissing && len(msgs) < limit && d.generated[threadID] < d.historyCap(threadID) {
		d.startBackfill(threadID, rec.E2EE, beforeMS, limit-len(msgs))
	}
	return msgs, nil
}

func (d *DemoBackend) startBackfill(threadID string, e2ee bool, beforeMS int64, want int) {
	key := threadID + "|" + strconv.FormatInt(beforeMS, 10)
	if d.backfills[key] {
		return
	}
	d.backfills[key] = true
	go func() {
		time.Sleep(demoBackfillDelay)
		d.mu.Lock()
		delete(d.backfills, key)
		if !d.loggedIn {
			d.mu.Unlock()
			return
		}
		known := d.store.MessageIDs(threadID)
		ceiling := beforeMS
		if ceiling == 0 {
			ceiling = math.MaxInt64
			if oldest, ok := d.store.OldestMessage(threadID); ok {
				ceiling = oldest.TimestampMS + 1
			}
		}
		d.generateOlder(threadID, e2ee, want)
		fresh := d.store.MessagesNotIn(threadID, ceiling, known)
		d.mu.Unlock()
		if len(fresh) > 0 {
			d.out.Emit(EventBackfilled, MessagesBackfilledEvent{ThreadID: threadID, Messages: fresh})
		}
	}()
}

func (d *DemoBackend) generateOlder(threadID string, e2ee bool, want int) {
	oldest, ok := d.store.OldestMessage(threadID)
	ts := time.Now().UnixMilli()
	if ok {
		ts = oldest.TimestampMS
	}
	n := min(max(want, 40), d.historyCap(threadID)-d.generated[threadID])
	mediaRich := threadID == d.mediaThreadID()
	for i := 0; i < n; i++ {
		ts -= int64(1+d.rng.IntN(120)) * 60_000
		if d.rng.IntN(6) == 0 {
			ts -= int64(5+d.rng.IntN(40)) * 3_600_000
		}
		msg := d.makeMessage(threadID, e2ee, ts, d.rng.IntN(3) == 0)
		if mediaRich && len(msg.Attachments) == 0 && !msg.Deleted && d.rng.IntN(10) < 6 {
			size := demoImageSizes[d.rng.IntN(len(demoImageSizes))]
			kind := "image"
			if d.rng.IntN(5) == 0 {
				kind = "video"
			}
			msg.Text = ""
			msg.Attachments = []Attachment{{ID: "img_0", Kind: kind, FileName: "IMG_" + strconv.Itoa(1000+d.rng.IntN(9000)) + ".png", MimeType: "image/png", Width: size[0], Height: size[1], Size: int64(size[0] * size[1] / 3)}}
			if kind == "video" {
				msg.Attachments[0].DurationMS = int64(3+d.rng.IntN(60)) * 1000
				msg.Attachments[0].Gif = d.rng.IntN(2) == 0
			}
		}
		d.store.PutMessage(msg)
	}
	d.generated[threadID] += n
}

func (d *DemoBackend) Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error) {
	d.mu.Lock()
	if err := d.requireLogin(); err != nil {
		d.mu.Unlock()
		return "", err
	}
	if !d.connected {
		d.mu.Unlock()
		return "", newError(CodeNotConnected, "not connected")
	}
	rec, ok := d.store.Thread(threadID)
	if !ok {
		d.mu.Unlock()
		return "", newError(CodeNotFound, "unknown thread %s", threadID)
	}
	if replyToID != "" {
		if _, found := d.store.FindMessage(replyToID); !found {
			d.mu.Unlock()
			return "", newError(CodeNotFound, "unknown reply target %s", replyToID)
		}
	}
	mentions, err := d.store.CheckMentions(threadID, text, mentions)
	if err != nil {
		d.mu.Unlock()
		return "", err
	}
	msg := Message{
		ID:          d.nextMessageID(rec.E2EE),
		ThreadID:    threadID,
		SenderID:    d.user.ID,
		SenderName:  d.user.Name,
		Text:        text,
		TimestampMS: time.Now().UnixMilli(),
		IsOutgoing:  true,
		Attachments: []Attachment{},
		ReplyToID:   replyToID,
		Mentions:    mentions,
	}
	d.store.PutMessage(msg)
	d.store.UpdateThread(threadID, func(rec *threadRec) {
		rec.LastActivityMS = msg.TimestampMS
		rec.Snippet = msg.Text
		rec.UnreadCount = 0
	})
	members := d.members[threadID]
	d.mu.Unlock()

	echo := d.store.Decorate(msg)
	echo.ClientID = clientID
	d.out.Emit(EventMessage, echo)
	if view, ok := d.store.ThreadView(threadID); ok {
		d.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
	if len(members) > 0 {
		reader := members[0]
		go func() {
			time.Sleep(3 * time.Second)
			d.mu.Lock()
			connected := d.connected
			d.mu.Unlock()
			if connected {
				d.out.Emit(EventRead, ReadEvent{ThreadID: threadID, UserID: reader, TimestampMS: time.Now().UnixMilli()})
			}
		}()
	}
	return msg.ID, nil
}

func (d *DemoBackend) MarkRead(ctx context.Context, threadID string) error {
	d.mu.Lock()
	if err := d.requireLogin(); err != nil {
		d.mu.Unlock()
		return err
	}
	if !d.store.HasThread(threadID) {
		d.mu.Unlock()
		return newError(CodeNotFound, "unknown thread %s", threadID)
	}
	d.store.UpdateThread(threadID, func(rec *threadRec) {
		rec.UnreadCount = 0
		rec.LastReadMS = rec.LastActivityMS
	})
	d.mu.Unlock()
	if view, ok := d.store.ThreadView(threadID); ok {
		d.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
	return nil
}

func (d *DemoBackend) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopLoop != nil {
		d.stopLoop()
		d.stopLoop = nil
	}
	d.connected = false
}
