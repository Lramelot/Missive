package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func parseWaveform(raw string) []float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var wrapped struct {
		Amplitudes []float64 `json:"amplitudes"`
	}
	if json.Unmarshal([]byte(raw), &wrapped) == nil && len(wrapped.Amplitudes) > 0 {
		return clampWaveform(wrapped.Amplitudes)
	}
	var plain []float64
	if json.Unmarshal([]byte(raw), &plain) == nil {
		return clampWaveform(plain)
	}
	return nil
}

func clampWaveform(values []float64) []float64 {
	peak := 0.0
	for _, v := range values {
		peak = max(peak, v)
	}
	out := make([]float64, len(values))
	for i, v := range values {
		switch {
		case peak > 1:
			out[i] = max(v, 0) / peak
		default:
			out[i] = min(max(v, 0), 1)
		}
	}
	return out
}

var geoPair = regexp.MustCompile(`(-?\d+(?:\.\d+)?)\s*,\s*(-?\d+(?:\.\d+)?)`)

func parseLatLong(candidates ...string) (*float64, *float64) {
	for _, c := range candidates {
		m := geoPair.FindStringSubmatch(c)
		if m == nil {
			continue
		}
		lat, err1 := strconv.ParseFloat(m[1], 64)
		long, err2 := strconv.ParseFloat(m[2], 64)
		if err1 == nil && err2 == nil && lat >= -90 && lat <= 90 && long >= -180 && long <= 180 {
			return &lat, &long
		}
	}
	return nil, nil
}

func mapsLink(lat, long *float64, fallback string) string {
	if lat != nil && long != nil {
		return fmt.Sprintf("https://www.google.com/maps/search/?api=1&query=%f,%f", *lat, *long)
	}
	return fallback
}

func callSystemText(ctaType, title, subtitle string) string {
	video := strings.Contains(ctaType, "video") || strings.Contains(strings.ToLower(title), "vid")
	missed := strings.Contains(ctaType, "missed") || strings.Contains(strings.ToLower(title), "missed") || strings.Contains(strings.ToLower(title), "manqué")
	switch {
	case missed && video:
		return "Appel vidéo manqué"
	case missed:
		return "Appel manqué"
	}
	label := "Appel audio"
	if video {
		label = "Appel vidéo"
	}
	if d := frenchDuration(subtitle); d != "" {
		return label + " · " + d
	}
	return label
}

var durationWords = strings.NewReplacer("mins", "min", "minutes", "min", "minute", "min", "secs", "s", "seconds", "s", "second", "s", "sec", "s", "hours", "h", "hour", "h", "hrs", "h", "hr", "h")

func frenchDuration(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !strings.ContainsAny(s, "0123456789") {
		return ""
	}
	return durationWords.Replace(s)
}

func xmaSpecial(id string, x *table.WrappedXMA) (Attachment, string, bool) {
	ctaType, native, action := "", "", ""
	if x.CTA != nil {
		ctaType, native, action = x.CTA.Type_, x.CTA.NativeUrl, x.CTA.ActionUrl
	}
	title, subtitle := firstNonEmpty(x.TitleText, x.HeaderTitle), firstNonEmpty(x.SubtitleText, x.HeaderSubtitleText)
	thumb := firstNonEmpty(x.PreviewUrlLarge, x.PreviewUrl, x.ImageUrl)
	lowerType := strings.ToLower(ctaType)
	switch {
	case strings.HasPrefix(ctaType, "xma_rtc_"):
		return Attachment{}, callSystemText(ctaType, title, subtitle), true
	case strings.Contains(lowerType, "location"):
		lat, long := parseLatLong(native, action, x.ActionUrl)
		return Attachment{
			ID: id, Kind: "location", Title: firstNonEmpty(title, "Position"), Description: subtitle,
			URL: mapsLink(lat, long, httpLink(action, x.ActionUrl)), ThumbnailURL: thumb, Latitude: lat, Longitude: long,
		}, "", true
	case strings.Contains(lowerType, "story"):
		return Attachment{
			ID: id, Kind: "story", Title: firstNonEmpty(title, "Story"), Description: subtitle,
			URL: httpLink(action, x.ActionUrl, native), ThumbnailURL: thumb,
		}, "", true
	case strings.Contains(lowerType, "contact"):
		return Attachment{ID: id, Kind: "contact", Title: firstNonEmpty(title, "Contact"), Description: subtitle, ThumbnailURL: thumb}, "", true
	}
	return Attachment{}, "", false
}

type adminPattern struct {
	re     *regexp.Regexp
	format string
}

var adminPatterns = []adminPattern{
	{regexp.MustCompile(`^You named the group (.+?)\.?$`), "Vous avez renommé le groupe « %s »"},
	{regexp.MustCompile(`^(.+?) named the group (.+?)\.?$`), "%s a renommé le groupe « %s »"},
	{regexp.MustCompile(`^You changed the group photo\.?$`), "Vous avez changé la photo du groupe"},
	{regexp.MustCompile(`^(.+?) changed the group photo\.?$`), "%s a changé la photo du groupe"},
	{regexp.MustCompile(`^You added (.+?)(?: to the group)?\.?$`), "Vous avez ajouté %s"},
	{regexp.MustCompile(`^(.+?) added (.+?)(?: to the group)?\.?$`), "%s a ajouté %s"},
	{regexp.MustCompile(`^You removed (.+?) from the group\.?$`), "Vous avez retiré %s du groupe"},
	{regexp.MustCompile(`^(.+?) removed (.+?) from the group\.?$`), "%s a retiré %s du groupe"},
	{regexp.MustCompile(`^You left the group\.?$`), "Vous avez quitté le groupe"},
	{regexp.MustCompile(`^(.+?) left the group\.?$`), "%s a quitté le groupe"},
	{regexp.MustCompile(`^(.+?) joined the group\.?$`), "%s a rejoint le groupe"},
	{regexp.MustCompile(`^You set the nickname for (.+?) to (.+?)\.?$`), "Vous avez défini le surnom de %s : %s"},
	{regexp.MustCompile(`^(.+?) set the nickname for (.+?) to (.+?)\.?$`), "%s a défini le surnom de %s : %s"},
	{regexp.MustCompile(`^You set your nickname to (.+?)\.?$`), "Vous avez défini votre surnom : %s"},
	{regexp.MustCompile(`^(.+?) set (?:his|her|their) (?:own )?nickname to (.+?)\.?$`), "%s a défini son surnom : %s"},
	{regexp.MustCompile(`^You changed the theme to (.+?)\.?$`), "Vous avez changé le thème : %s"},
	{regexp.MustCompile(`^(.+?) changed the theme to (.+?)\.?$`), "%s a changé le thème : %s"},
	{regexp.MustCompile(`^You set the quick reaction to (.+?)\.?$`), "Vous avez défini la réaction rapide sur %s"},
	{regexp.MustCompile(`^(.+?) set the quick reaction to (.+?)\.?$`), "%s a défini la réaction rapide sur %s"},
	{regexp.MustCompile(`^You pinned a message\.?$`), "Vous avez épinglé un message"},
	{regexp.MustCompile(`^(.+?) pinned a message\.?$`), "%s a épinglé un message"},
	{regexp.MustCompile(`^You unpinned a message\.?$`), "Vous avez désépinglé un message"},
	{regexp.MustCompile(`^(.+?) unpinned a message\.?$`), "%s a désépinglé un message"},
	{regexp.MustCompile(`^You created a poll: (.+?)\.?$`), "Vous avez créé un sondage : %s"},
	{regexp.MustCompile(`^(.+?) created a poll: (.+?)\.?$`), "%s a créé un sondage : %s"},
	{regexp.MustCompile(`^You voted for "?(.+?)"? in the poll(?:: .+)?\.?$`), "Vous avez voté pour « %s »"},
	{regexp.MustCompile(`^(.+?) voted for "?(.+?)"? in the poll(?:: .+)?\.?$`), "%s a voté pour « %s »"},
	{regexp.MustCompile(`^You made (.+?) an admin\.?$`), "Vous avez nommé %s administrateur"},
	{regexp.MustCompile(`^(.+?) made (.+?) an admin\.?$`), "%s a nommé %s administrateur"},
	{regexp.MustCompile(`^You turned on disappearing messages\.?.*$`), "Vous avez activé les messages éphémères"},
	{regexp.MustCompile(`^(.+?) turned on disappearing messages\.?.*$`), "%s a activé les messages éphémères"},
	{regexp.MustCompile(`^You turned off disappearing messages\.?$`), "Vous avez désactivé les messages éphémères"},
	{regexp.MustCompile(`^(.+?) turned off disappearing messages\.?$`), "%s a désactivé les messages éphémères"},
	{regexp.MustCompile(`^You missed (?:a|an) (video )?call from (.+?)\.?$`), "Appel manqué"},
	{regexp.MustCompile(`^(.+?) missed (?:a|an) (?:video )?call(?: from you)?\.?$`), "Appel manqué"},
}

func translateAdminText(text string) string {
	trimmed := strings.TrimSpace(text)
	for _, p := range adminPatterns {
		m := p.re.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		args := make([]any, 0, len(m)-1)
		for _, g := range m[1:] {
			args = append(args, g)
		}
		want := strings.Count(p.format, "%s")
		if len(args) > want {
			args = args[len(args)-want:]
		}
		return fmt.Sprintf(p.format, args...)
	}
	return trimmed
}
