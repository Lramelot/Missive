package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const groupGapMS = 5 * 60 * 1000

const (
	LocaleFrench  = "fr"
	LocaleEnglish = "en"
)

type exportText struct {
	lang            string
	weekdays        [7]string
	months          [12]string
	exportedMeta    string
	footer          string
	bubbleLayout    string
	you             string
	unknown         string
	attachments     map[string]string
	voice           string
	attachment      string
	forwardedTag    string
	forwardedInline string
	removed         string
	edited          string
	poll            string
	replyFallback   string
	replyIntro      string
	replyFallbackMD string
	reactions       string
	mediaSuffix     string
}

var frenchExportText = exportText{
	lang:            LocaleFrench,
	weekdays:        [7]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"},
	months:          [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
	exportedMeta:    "%s · exporté le %s à %s depuis Missive",
	footer:          "Export Missive · %s",
	bubbleLayout:    "02/01/2006 15:04",
	you:             "Vous",
	unknown:         "Inconnu",
	attachments:     map[string]string{"image": "Photo", "video": "Vidéo", "audio": "Audio", "file": "Fichier", "sticker": "Autocollant", "link": "Lien", "location": "Position", "contact": "Contact", "story": "Story"},
	voice:           "Message vocal",
	attachment:      "Pièce jointe",
	forwardedTag:    "Transféré",
	forwardedInline: "transféré",
	removed:         "Message retiré",
	edited:          "modifié",
	poll:            "Sondage : ",
	replyFallback:   "Message",
	replyIntro:      "En réponse à %s : ",
	replyFallbackMD: "un message",
	reactions:       "Réactions : ",
	mediaSuffix:     " - médias",
}

var englishExportText = exportText{
	lang:            LocaleEnglish,
	weekdays:        [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
	months:          [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
	exportedMeta:    "%s · exported on %s at %s from Missive",
	footer:          "Missive export · %s",
	bubbleLayout:    "2006-01-02 15:04",
	you:             "You",
	unknown:         "Unknown",
	attachments:     map[string]string{"image": "Photo", "video": "Video", "audio": "Audio", "file": "File", "sticker": "Sticker", "link": "Link", "location": "Location", "contact": "Contact", "story": "Story"},
	voice:           "Voice message",
	attachment:      "Attachment",
	forwardedTag:    "Forwarded",
	forwardedInline: "forwarded",
	removed:         "Message removed",
	edited:          "edited",
	poll:            "Poll: ",
	replyFallback:   "Message",
	replyIntro:      "In reply to %s: ",
	replyFallbackMD: "a message",
	reactions:       "Reactions: ",
	mediaSuffix:     " - media",
}

func exportTextFor(locale string) *exportText {
	lang := strings.ToLower(strings.TrimSpace(locale))
	if lang == LocaleEnglish || strings.HasPrefix(lang, LocaleEnglish+"-") || strings.HasPrefix(lang, LocaleEnglish+"_") {
		return &englishExportText
	}
	return &frenchExportText
}

type exportDoc struct {
	Title      string
	ExportedAt time.Time
	Messages   []Message
	Files      map[string]string
	Thread     threadRec
	Locale     string
}

func (d exportDoc) file(msg Message, att Attachment) string {
	return d.Files[mediaKey(msg.ID, att.ID)]
}

func (d exportDoc) text() *exportText {
	return exportTextFor(d.Locale)
}

func (x *exportText) day(t time.Time) string {
	if x.lang == LocaleEnglish {
		return fmt.Sprintf("%s, %s %d, %d", x.weekdays[t.Weekday()], x.months[t.Month()-1], t.Day(), t.Year())
	}
	return fmt.Sprintf("%s %d %s %d", x.weekdays[t.Weekday()], t.Day(), x.months[t.Month()-1], t.Year())
}

func (x *exportText) date(t time.Time) string {
	if x.lang == LocaleEnglish {
		return fmt.Sprintf("%s %d, %d", x.months[t.Month()-1], t.Day(), t.Year())
	}
	return fmt.Sprintf("%d %s %d", t.Day(), x.months[t.Month()-1], t.Year())
}

func (x *exportText) exportedLine(doc exportDoc) string {
	return fmt.Sprintf(x.exportedMeta, x.plural(len(doc.Messages), "message", "messages"), x.date(doc.ExportedAt), doc.ExportedAt.Format("15:04"))
}

func (x *exportText) count(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 && x.lang == LocaleEnglish {
			b.WriteString(",")
		} else if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (x *exportText) plural(n int, one, many string) string {
	if n == 1 || (n <= 1 && x.lang != LocaleEnglish) {
		return x.count(n) + " " + one
	}
	return x.count(n) + " " + many
}

func localTime(ms int64) time.Time {
	return time.UnixMilli(ms).Local()
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func (x *exportText) senderLabel(msg Message) string {
	if name := strings.TrimSpace(msg.SenderName); name != "" {
		return name
	}
	if msg.IsOutgoing {
		return x.you
	}
	return x.unknown
}

func (x *exportText) attachmentLabel(att Attachment) string {
	if att.Kind == "audio" && att.Voice {
		return x.voice
	}
	if label, ok := x.attachments[att.Kind]; ok {
		return label
	}
	return x.attachment
}

func webURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

func relHref(rel string) string {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func esc(s string) string {
	return html.EscapeString(s)
}

const exportCSS = `:root{color-scheme:light dark;--bg:#f6f6f4;--text:#1c1c1e;--muted:#6c6c72;--in:#e9e9ec;--out:#2f6bff;--out-text:#fff;--line:#dedee3;--chip:rgba(120,120,128,.14)}
@media (prefers-color-scheme:dark){:root{--bg:#141416;--text:#ececf0;--muted:#9c9ca4;--in:#2a2a2f;--out:#2a5fe0;--out-text:#fff;--line:#2e2e33;--chip:rgba(160,160,170,.18)}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.45 "Segoe UI",system-ui,-apple-system,"Helvetica Neue",Arial,sans-serif}
header,main,footer{max-width:780px;margin:0 auto;padding:0 20px}
header{padding-top:36px;padding-bottom:16px;border-bottom:1px solid var(--line)}
h1{font-size:26px;line-height:1.2;margin:0 0 6px;overflow-wrap:anywhere}
.meta{margin:0;color:var(--muted);font-size:13px}
.day{margin:28px 0 10px;text-align:center;color:var(--muted);font-size:12px;font-weight:600}
.msg{display:flex;flex-direction:column;align-items:flex-start;margin:2px 0}
.msg.first{margin-top:12px}
.msg.out{align-items:flex-end}
.who{margin:0 14px 3px;color:var(--muted);font-size:12px}
.bubble{max-width:78%;padding:8px 14px;border-radius:18px;background:var(--in);overflow-wrap:anywhere}
.out .bubble{background:var(--out);color:var(--out-text)}
.out .bubble a{color:inherit}
.text{margin:0;white-space:pre-wrap}
.deleted{font-style:italic;opacity:.75}
.reply{margin:2px 0 6px;padding:2px 10px;border-left:3px solid currentColor;opacity:.75;font-size:13px;white-space:pre-wrap}
.reply .rs{display:block;font-weight:600}
.tag{font-size:11px;opacity:.75;margin-bottom:2px}
.bubble img,.bubble video{display:block;max-width:100%;max-height:440px;margin:6px 0 2px;border-radius:12px}
.bubble audio{display:block;margin:6px 0 2px;max-width:100%}
.att,.file{display:inline-block;margin:4px 0;padding:6px 10px;border-radius:12px;background:var(--chip);color:inherit;text-decoration:none}
.link small{display:block;opacity:.8}
.edited{font-size:11px;opacity:.7;margin-left:6px}
.reactions{margin:3px 14px 0;color:var(--muted);font-size:12px}
.system{margin:12px 0;text-align:center;color:var(--muted);font-size:13px}
.poll{margin:4px 0 0;padding-left:18px}
footer{padding-top:28px;padding-bottom:36px;color:var(--muted);font-size:12px}
@media print{:root{--bg:#fff;--text:#000;--muted:#555;--in:#efeff1;--out:#dde6fb;--out-text:#000;--line:#ccc}body{font-size:12px}.msg{break-inside:avoid}.bubble img,.bubble video{max-height:300px}}
`

func renderExportHTML(w io.Writer, doc exportDoc) error {
	x := doc.text()
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"" + x.lang + "\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' file: data:; media-src 'self' file:\">\n")
	b.WriteString("<meta name=\"color-scheme\" content=\"light dark\">\n")
	b.WriteString("<title>" + esc(doc.Title) + "</title>\n<style>\n" + exportCSS + "</style>\n</head>\n<body>\n<header>\n")
	b.WriteString("<h1>" + esc(doc.Title) + "</h1>\n")
	b.WriteString("<p class=\"meta\">" + esc(x.exportedLine(doc)) + "</p>\n")
	b.WriteString("</header>\n<main>\n")
	var prev *Message
	for i := range doc.Messages {
		msg := doc.Messages[i]
		t := localTime(msg.TimestampMS)
		newDay := prev == nil || !sameDay(localTime(prev.TimestampMS), t)
		if newDay {
			b.WriteString("<h2 class=\"day\">" + esc(x.day(t)) + "</h2>\n")
		}
		if msg.System {
			b.WriteString("<p class=\"system\">" + esc(msg.Text) + " · " + t.Format("15:04") + "</p>\n")
			prev = &doc.Messages[i]
			continue
		}
		first := newDay || prev.System || prev.SenderID != msg.SenderID || prev.IsOutgoing != msg.IsOutgoing || msg.TimestampMS-prev.TimestampMS > groupGapMS
		class := "msg in"
		if msg.IsOutgoing {
			class = "msg out"
		}
		if first {
			class += " first"
		}
		b.WriteString("<article class=\"" + class + "\" id=\"m-" + esc(msg.ID) + "\">\n")
		if first {
			b.WriteString("<div class=\"who\">" + esc(x.senderLabel(msg)) + " · <time datetime=\"" + t.Format(time.RFC3339) + "\">" + t.Format("15:04") + "</time></div>\n")
		}
		b.WriteString("<div class=\"bubble\" title=\"" + t.Format(x.bubbleLayout) + "\">")
		writeHTMLBody(&b, doc, msg)
		b.WriteString("</div>\n")
		if len(msg.Reactions) > 0 {
			parts := make([]string, 0, len(msg.Reactions))
			for _, r := range msg.Reactions {
				parts = append(parts, esc(r.Emoji)+" "+strconv.Itoa(max(r.Count, 1)))
			}
			b.WriteString("<div class=\"reactions\">" + strings.Join(parts, " · ") + "</div>\n")
		}
		b.WriteString("</article>\n")
		prev = &doc.Messages[i]
	}
	b.WriteString("</main>\n<footer>" + esc(fmt.Sprintf(x.footer, x.date(doc.ExportedAt))) + "</footer>\n</body>\n</html>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func writeHTMLBody(b *strings.Builder, doc exportDoc, msg Message) {
	x := doc.text()
	if msg.Forwarded {
		b.WriteString("<div class=\"tag\">" + esc(x.forwardedTag) + "</div>")
	}
	if msg.ReplyTo != nil {
		quoted := msg.ReplyTo.Text
		if strings.TrimSpace(quoted) == "" && msg.ReplyTo.Kind != "" {
			quoted = x.attachmentLabel(Attachment{Kind: msg.ReplyTo.Kind})
		}
		b.WriteString("<blockquote class=\"reply\"><span class=\"rs\">" + esc(cmpOr(msg.ReplyTo.SenderName, x.replyFallback)) + "</span>" + esc(quoted) + "</blockquote>")
	}
	if msg.Deleted {
		b.WriteString("<p class=\"text deleted\">" + esc(x.removed) + "</p>")
		return
	}
	if msg.Text != "" {
		b.WriteString("<p class=\"text\">" + esc(msg.Text) + "</p>")
	}
	for _, att := range msg.Attachments {
		writeHTMLAttachment(b, x, doc.file(msg, att), att)
	}
	if msg.Poll != nil {
		b.WriteString("<p class=\"text\">" + esc(x.poll) + esc(msg.Poll.Question) + "</p><ul class=\"poll\">")
		for _, opt := range msg.Poll.Options {
			b.WriteString("<li>" + esc(opt.Text) + " — " + esc(x.plural(opt.VoteCount, "vote", "votes")) + "</li>")
		}
		b.WriteString("</ul>")
	}
	if msg.Edited {
		b.WriteString("<span class=\"edited\">" + esc(x.edited) + "</span>")
	}
}

func writeHTMLAttachment(b *strings.Builder, x *exportText, rel string, att Attachment) {
	label := x.attachmentLabel(att)
	name := cmpOr(att.FileName, label)
	if rel != "" {
		href := esc(relHref(rel))
		switch att.Kind {
		case "image", "sticker":
			b.WriteString("<a href=\"" + href + "\"><img src=\"" + href + "\" alt=\"" + esc(name) + "\" loading=\"lazy\"></a>")
		case "video":
			b.WriteString("<video controls preload=\"metadata\" src=\"" + href + "\"></video>")
		case "audio":
			b.WriteString("<audio controls preload=\"none\" src=\"" + href + "\"></audio>")
		default:
			b.WriteString("<a class=\"file\" href=\"" + href + "\">" + esc(name) + "</a>")
		}
		return
	}
	switch att.Kind {
	case "link", "location":
		title := cmpOr(att.Title, label)
		if att.Kind == "location" && att.Latitude != nil && att.Longitude != nil {
			title = fmt.Sprintf("%s (%.5f, %.5f)", title, *att.Latitude, *att.Longitude)
		}
		inner := esc(title)
		if att.Description != "" {
			inner += "<small>" + esc(att.Description) + "</small>"
		}
		if href := webURL(att.URL); href != "" {
			b.WriteString("<a class=\"att link\" href=\"" + esc(href) + "\" rel=\"noopener noreferrer\">" + inner + "</a>")
			return
		}
		b.WriteString("<span class=\"att link\">" + inner + "</span>")
	default:
		text := label
		if att.FileName != "" {
			text += " · " + att.FileName
		} else if att.Title != "" {
			text += " · " + att.Title
		}
		b.WriteString("<span class=\"att\">" + esc(text) + "</span>")
	}
}

func cmpOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

var markdownEscaper = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", `*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`,
	`<`, `\<`, `>`, `\>`, `#`, `\#`, `|`, `\|`, `~`, `\~`, `&`, `&amp;`,
)

var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", " ", " ", " ", " ")

func mdEscape(s string) string {
	return markdownEscaper.Replace(lineBreaks.Replace(s))
}

func mdText(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, line := range lines {
		line = mdEscape(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := line[indent:]
		switch {
		case strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "="):
			line = line[:indent] + `\` + trimmed
		case orderedMarkerEnd(trimmed) > 0:
			end := indent + orderedMarkerEnd(trimmed)
			line = line[:end] + `\` + line[end:]
		}
		lines[i] = line
	}
	return strings.Join(lines, "  \n")
}

func orderedMarkerEnd(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i < len(s) && (s[i] == '.' || s[i] == ')') {
		return i
	}
	return 0
}

func renderExportMarkdown(w io.Writer, doc exportDoc) error {
	x := doc.text()
	var b strings.Builder
	b.WriteString("# " + mdEscape(doc.Title) + "\n\n")
	b.WriteString("_" + x.exportedLine(doc) + "_\n")
	var prev *Message
	for i := range doc.Messages {
		msg := doc.Messages[i]
		t := localTime(msg.TimestampMS)
		if prev == nil || !sameDay(localTime(prev.TimestampMS), t) {
			b.WriteString("\n## " + x.day(t) + "\n")
		}
		prev = &doc.Messages[i]
		if msg.System {
			b.WriteString("\n_" + mdEscape(msg.Text) + " · " + t.Format("15:04") + "_\n")
			continue
		}
		b.WriteString("\n**" + mdEscape(x.senderLabel(msg)) + "** · " + t.Format("15:04"))
		if msg.Forwarded {
			b.WriteString(" · " + x.forwardedInline)
		}
		if msg.Edited {
			b.WriteString(" · " + x.edited)
		}
		b.WriteString("\n")
		if msg.ReplyTo != nil {
			quoted := msg.ReplyTo.Text
			if strings.TrimSpace(quoted) == "" && msg.ReplyTo.Kind != "" {
				quoted = x.attachmentLabel(Attachment{Kind: msg.ReplyTo.Kind})
			}
			b.WriteString("> " + fmt.Sprintf(x.replyIntro, mdEscape(cmpOr(msg.ReplyTo.SenderName, x.replyFallbackMD))) + strings.ReplaceAll(mdText(quoted), "\n", "\n> ") + "\n")
			b.WriteString("\n")
		}
		if msg.Deleted {
			b.WriteString("_" + x.removed + "_\n")
			continue
		}
		if msg.Text != "" {
			b.WriteString(mdText(msg.Text) + "\n")
		}
		for _, att := range msg.Attachments {
			b.WriteString(markdownAttachment(x, doc.file(msg, att), att) + "\n")
		}
		if msg.Poll != nil {
			b.WriteString(x.poll + mdEscape(msg.Poll.Question) + "\n")
			for _, opt := range msg.Poll.Options {
				b.WriteString("- " + mdEscape(opt.Text) + " — " + x.plural(opt.VoteCount, "vote", "votes") + "\n")
			}
		}
		if len(msg.Reactions) > 0 {
			parts := make([]string, 0, len(msg.Reactions))
			for _, r := range msg.Reactions {
				parts = append(parts, mdEscape(r.Emoji)+" "+strconv.Itoa(max(r.Count, 1)))
			}
			b.WriteString("\n" + x.reactions + strings.Join(parts, " · ") + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func markdownAttachment(x *exportText, rel string, att Attachment) string {
	label := x.attachmentLabel(att)
	name := mdEscape(cmpOr(att.FileName, label))
	if rel != "" {
		if att.Kind == "image" || att.Kind == "sticker" {
			return "![" + name + "](" + relHref(rel) + ")"
		}
		return "[" + label + " · " + name + "](" + relHref(rel) + ")"
	}
	if att.Kind == "link" || att.Kind == "location" {
		title := mdEscape(cmpOr(att.Title, label))
		if href := webURL(att.URL); href != "" {
			return "[" + title + "](<" + strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20").Replace(href) + ">)"
		}
		return title
	}
	if att.FileName != "" {
		return "_[" + label + " · " + name + "]_"
	}
	return "_[" + label + "]_"
}

func renderExportJSON(w io.Writer, doc exportDoc) error {
	out := make([]exportedMessage, len(doc.Messages))
	for i, msg := range doc.Messages {
		out[i] = exportedMessage{Message: msg}
		for _, att := range msg.Attachments {
			if rel := doc.file(msg, att); rel != "" {
				if out[i].LocalFiles == nil {
					out[i].LocalFiles = make(map[string]string)
				}
				out[i].LocalFiles[att.ID] = rel
			}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
