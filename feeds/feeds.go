package feeds

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/status/storage"
)

// RSS 2.0 Feed with proper namespaces
type RSSFeed struct {
	XMLName       xml.Name   `xml:"rss"`
	Version       string     `xml:"version,attr"`
	AtomNS        string     `xml:"xmlns:atom,attr"`
	ContentNS     string     `xml:"xmlns:content,attr,omitempty"`
	DcNS          string     `xml:"xmlns:dc,attr,omitempty"`
	Channel       RSSChannel `xml:"channel"`
}

type RSSChannel struct {
	Title          string      `xml:"title"`
	Link           string      `xml:"link"`
	Description    string      `xml:"description"`
	Language       string      `xml:"language"`
	Copyright      string      `xml:"copyright,omitempty"`
	ManagingEditor string      `xml:"managingEditor,omitempty"`
	WebMaster      string      `xml:"webMaster,omitempty"`
	PubDate        string      `xml:"pubDate"`
	LastBuildDate  string      `xml:"lastBuildDate"`
	Category       string      `xml:"category,omitempty"`
	Generator      string      `xml:"generator"`
	Docs           string      `xml:"docs"`
	TTL            int         `xml:"ttl"`
	Image          *RSSImage   `xml:"image,omitempty"`
	AtomLink       *RSSAtomLink `xml:"atom:link,omitempty"`
	Items          []RSSItem   `xml:"item"`
}

type RSSImage struct {
	URL   string `xml:"url"`
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type RSSItem struct {
	Title          string `xml:"title"`
	Link           string `xml:"link"`
	Description    string `xml:"description"`
	Author         string `xml:"author,omitempty"`
	Category       string `xml:"category,omitempty"`
	Comments       string `xml:"comments,omitempty"`
	Enclosure      string `xml:"enclosure,omitempty"`
	GUID           RSSGUID `xml:"guid"`
	PubDate        string `xml:"pubDate"`
	Source         string `xml:"source,omitempty"`
	ContentEncoded string `xml:"content:encoded,omitempty"`
}

type RSSGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

// Atom 1.0 Feed
type AtomFeed struct {
	XMLName   xml.Name    `xml:"feed"`
	Xmlns     string      `xml:"xmlns,attr"`
	Title     string      `xml:"title"`
	Subtitle  string      `xml:"subtitle,omitempty"`
	Link      []AtomLink  `xml:"link"`
	Updated   string      `xml:"updated"`
	ID        string      `xml:"id"`
	Author    *AtomAuthor `xml:"author,omitempty"`
	Rights    string      `xml:"rights,omitempty"`
	Generator *AtomGenerator `xml:"generator,omitempty"`
	Icon      string      `xml:"icon,omitempty"`
	Logo      string      `xml:"logo,omitempty"`
	Entries   []AtomEntry `xml:"entry"`
}

// AtomLink for RSS feeds (used in atom:link)
type RSSAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

// AtomLink for Atom feeds
type AtomLink struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr,omitempty"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
}

type AtomAuthor struct {
	Name  string `xml:"name"`
	Email string `xml:"email,omitempty"`
	URI   string `xml:"uri,omitempty"`
}

type AtomGenerator struct {
	Value   string `xml:",chardata"`
	URI     string `xml:"uri,attr,omitempty"`
	Version string `xml:"version,attr,omitempty"`
}

type AtomEntry struct {
	Title     string        `xml:"title"`
	Link      []AtomLink    `xml:"link"`
	ID        string        `xml:"id"`
	Updated   string        `xml:"updated"`
	Published string        `xml:"published"`
	Author    *AtomAuthor   `xml:"author,omitempty"`
	Summary   *AtomContent  `xml:"summary,omitempty"`
	Content   *AtomContent  `xml:"content,omitempty"`
	Category  []AtomCategory `xml:"category,omitempty"`
}

type AtomContent struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

type AtomCategory struct {
	Term   string `xml:"term,attr"`
	Label  string `xml:"label,attr,omitempty"`
	Scheme string `xml:"scheme,attr,omitempty"`
}

// JSON Feed 1.1
type JSONFeed struct {
	Version     string          `json:"version"`
	Title       string          `json:"title"`
	HomePageURL string          `json:"home_page_url"`
	FeedURL     string          `json:"feed_url"`
	Description string          `json:"description,omitempty"`
	UserComment string          `json:"user_comment,omitempty"`
	NextURL     string          `json:"next_url,omitempty"`
	Icon        string          `json:"icon,omitempty"`
	Favicon     string          `json:"favicon,omitempty"`
	Authors     []JSONAuthor    `json:"authors,omitempty"`
	Language    string          `json:"language,omitempty"`
	Expired     bool            `json:"expired,omitempty"`
	Hubs        []JSONHub       `json:"hubs,omitempty"`
	Items       []JSONFeedItem  `json:"items"`
}

type JSONAuthor struct {
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

type JSONHub struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type JSONFeedItem struct {
	ID            string       `json:"id"`
	URL           string       `json:"url,omitempty"`
	ExternalURL   string       `json:"external_url,omitempty"`
	Title         string       `json:"title"`
	ContentHTML   string       `json:"content_html,omitempty"`
	ContentText   string       `json:"content_text,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	Image         string       `json:"image,omitempty"`
	BannerImage   string       `json:"banner_image,omitempty"`
	DatePublished string       `json:"date_published"`
	DateModified  string       `json:"date_modified,omitempty"`
	Authors       []JSONAuthor `json:"authors,omitempty"`
	Tags          []string     `json:"tags,omitempty"`
	Language      string       `json:"language,omitempty"`
	Attachments   []JSONAttachment `json:"attachments,omitempty"`
}

type JSONAttachment struct {
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
	Title    string `json:"title,omitempty"`
	Size     int64  `json:"size_in_bytes,omitempty"`
	Duration int    `json:"duration_in_seconds,omitempty"`
}

// StatusSummary for current system status in feeds
type StatusSummary struct {
	Overall     string
	Operational int
	Degraded    int
	Down        int
	Total       int
}

// FeedGenerator generates various feed formats
type FeedGenerator struct {
	title       string
	baseURL     string
	description string
	copyright   string
	author      string
	email       string
}

// NewFeedGenerator creates a new feed generator
func NewFeedGenerator(title, baseURL string) *FeedGenerator {
	return &FeedGenerator{
		title:       title,
		baseURL:     baseURL,
		description: "System status updates, incidents, and maintenance notifications",
		copyright:   fmt.Sprintf("© %d %s. All rights reserved.", time.Now().Year(), title),
		author:      "Status Monitor",
		email:       "status@example.com",
	}
}

// SetDescription sets custom feed description
func (fg *FeedGenerator) SetDescription(desc string) {
	fg.description = desc
}

// SetCopyright sets custom copyright notice
func (fg *FeedGenerator) SetCopyright(copyright string) {
	fg.copyright = copyright
}

// SetAuthor sets feed author information
func (fg *FeedGenerator) SetAuthor(name, email string) {
	fg.author = name
	fg.email = email
}

// GenerateRSS generates RSS 2.0 feed from incidents
func (fg *FeedGenerator) GenerateRSS(incidents []storage.Incident) ([]byte, error) {
	return fg.GenerateRSSWithStatus(incidents, nil)
}

// GenerateRSSWithStatus generates RSS 2.0 feed with optional status summary
func (fg *FeedGenerator) GenerateRSSWithStatus(incidents []storage.Incident, status *StatusSummary) ([]byte, error) {
	now := time.Now()
	items := make([]RSSItem, 0, len(incidents)+1)

	// Add current status summary as first item if provided
	if status != nil {
		statusItem := RSSItem{
			Title:       fg.formatStatusTitle(status),
			Link:        fg.baseURL,
			Description: fg.formatStatusDescription(status),
			GUID:        RSSGUID{Value: fmt.Sprintf("%s/status/%s", fg.baseURL, now.Format("2006-01-02")), IsPermaLink: false},
			PubDate:     now.Format(time.RFC1123Z),
			Category:    "status",
			ContentEncoded: fg.formatStatusHTML(status),
		}
		items = append(items, statusItem)
	}

	// Add incidents
	for _, inc := range incidents {
		item := RSSItem{
			Title:       fg.formatIncidentTitle(inc),
			Link:        fmt.Sprintf("%s/incidents/%s", fg.baseURL, inc.ID),
			Description: fg.formatIncidentDescription(inc),
			GUID:        RSSGUID{Value: fmt.Sprintf("urn:incident:%s", inc.ID), IsPermaLink: false},
			PubDate:     inc.CreatedAt.Format(time.RFC1123Z),
			Category:    fg.mapSeverityToCategory(inc.Severity),
			ContentEncoded: fg.formatIncidentHTML(inc),
		}
		items = append(items, item)
	}

	var pubDate string
	if len(incidents) > 0 {
		pubDate = incidents[0].CreatedAt.Format(time.RFC1123Z)
	} else {
		pubDate = now.Format(time.RFC1123Z)
	}

	feed := RSSFeed{
		Version:   "2.0",
		AtomNS:    "http://www.w3.org/2005/Atom",
		ContentNS: "http://purl.org/rss/1.0/modules/content/",
		DcNS:      "http://purl.org/dc/elements/1.1/",
		Channel: RSSChannel{
			Title:         fg.title + " - Status Updates",
			Link:          fg.baseURL,
			Description:   fg.description,
			Language:      "en-us",
			Copyright:     fg.copyright,
			PubDate:       pubDate,
			LastBuildDate: now.Format(time.RFC1123Z),
			Generator:     "Status Monitor v1.0",
			Docs:          "https://www.rssboard.org/rss-specification",
			TTL:           5, // 5 minutes
			Image: &RSSImage{
				URL:   fg.baseURL + "/static/logo.svg",
				Title: fg.title,
				Link:  fg.baseURL,
			},
			AtomLink: &RSSAtomLink{
				Href: fg.baseURL + "/feed/rss",
				Rel:  "self",
				Type: "application/rss+xml",
			},
			Items: items,
		},
	}

	output, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, err
	}

	return output, nil
}

// GenerateAtom generates Atom 1.0 feed from incidents
func (fg *FeedGenerator) GenerateAtom(incidents []storage.Incident) ([]byte, error) {
	return fg.GenerateAtomWithStatus(incidents, nil)
}

// GenerateAtomWithStatus generates Atom 1.0 feed with optional status summary
func (fg *FeedGenerator) GenerateAtomWithStatus(incidents []storage.Incident, status *StatusSummary) ([]byte, error) {
	now := time.Now()
	entries := make([]AtomEntry, 0, len(incidents)+1)

	// Add current status summary as first entry if provided
	if status != nil {
		statusEntry := AtomEntry{
			Title: fg.formatStatusTitle(status),
			Link: []AtomLink{
				{Href: fg.baseURL, Rel: "alternate", Type: "text/html"},
			},
			ID:        fmt.Sprintf("tag:%s,%s:status", extractDomain(fg.baseURL), now.Format("2006-01-02")),
			Updated:   now.Format(time.RFC3339),
			Published: now.Format(time.RFC3339),
			Summary:   &AtomContent{Type: "text", Value: fg.formatStatusDescription(status)},
			Content:   &AtomContent{Type: "html", Value: fg.formatStatusHTML(status)},
			Category: []AtomCategory{
				{Term: "status", Label: "System Status"},
			},
		}
		entries = append(entries, statusEntry)
	}

	// Add incidents
	for _, inc := range incidents {
		entry := AtomEntry{
			Title: fg.formatIncidentTitle(inc),
			Link: []AtomLink{
				{Href: fmt.Sprintf("%s/incidents/%s", fg.baseURL, inc.ID), Rel: "alternate", Type: "text/html"},
			},
			ID:        fmt.Sprintf("tag:%s,%s:incident:%s", extractDomain(fg.baseURL), inc.CreatedAt.Format("2006-01-02"), inc.ID),
			Updated:   inc.UpdatedAt.Format(time.RFC3339),
			Published: inc.CreatedAt.Format(time.RFC3339),
			Author:    &AtomAuthor{Name: fg.author},
			Summary:   &AtomContent{Type: "text", Value: inc.Message},
			Content:   &AtomContent{Type: "html", Value: fg.formatIncidentHTML(inc)},
			Category: []AtomCategory{
				{Term: inc.Severity, Label: fg.mapSeverityToLabel(inc.Severity)},
				{Term: inc.Status, Label: fg.mapStatusToLabel(inc.Status)},
			},
		}
		entries = append(entries, entry)
	}

	var updated string
	if len(incidents) > 0 {
		updated = incidents[0].UpdatedAt.Format(time.RFC3339)
	} else {
		updated = now.Format(time.RFC3339)
	}

	feed := AtomFeed{
		Xmlns:    "http://www.w3.org/2005/Atom",
		Title:    fg.title + " - Status Updates",
		Subtitle: fg.description,
		Link: []AtomLink{
			{Href: fg.baseURL, Rel: "alternate", Type: "text/html"},
			{Href: fg.baseURL + "/feed/atom", Rel: "self", Type: "application/atom+xml"},
			{Href: fg.baseURL + "/feed/rss", Rel: "alternate", Type: "application/rss+xml", Title: "RSS Feed"},
			{Href: fg.baseURL + "/feed/json", Rel: "alternate", Type: "application/feed+json", Title: "JSON Feed"},
		},
		Updated: updated,
		ID:      fg.baseURL,
		Author:  &AtomAuthor{Name: fg.author, URI: fg.baseURL},
		Rights:  fg.copyright,
		Generator: &AtomGenerator{
			Value:   "Status Monitor",
			URI:     "https://github.com/status",
			Version: "1.0",
		},
		Icon:    fg.baseURL + "/favicon.svg",
		Logo:    fg.baseURL + "/static/logo.svg",
		Entries: entries,
	}

	output, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, err
	}

	return append([]byte(xml.Header), output...), nil
}

// GenerateJSON generates JSON Feed 1.1 from incidents
func (fg *FeedGenerator) GenerateJSON(incidents []storage.Incident) ([]byte, error) {
	return fg.GenerateJSONWithStatus(incidents, nil)
}

// GenerateJSONWithStatus generates JSON Feed 1.1 with optional status summary
func (fg *FeedGenerator) GenerateJSONWithStatus(incidents []storage.Incident, status *StatusSummary) ([]byte, error) {
	now := time.Now()
	items := make([]JSONFeedItem, 0, len(incidents)+1)

	// Add current status summary as first item if provided
	if status != nil {
		statusItem := JSONFeedItem{
			ID:            fmt.Sprintf("%s/status/%s", fg.baseURL, now.Format("2006-01-02")),
			URL:           fg.baseURL,
			Title:         fg.formatStatusTitle(status),
			ContentHTML:   fg.formatStatusHTML(status),
			ContentText:   fg.formatStatusDescription(status),
			Summary:       fg.formatStatusSummary(status),
			DatePublished: now.Format(time.RFC3339),
			DateModified:  now.Format(time.RFC3339),
			Tags:          []string{"status", status.Overall},
			Language:      "en",
		}
		items = append(items, statusItem)
	}

	// Add incidents
	for _, inc := range incidents {
		tags := []string{inc.Severity, inc.Status}
		tags = append(tags, inc.AffectedServices...)

		item := JSONFeedItem{
			ID:            fmt.Sprintf("%s/incidents/%s", fg.baseURL, inc.ID),
			URL:           fmt.Sprintf("%s/incidents/%s", fg.baseURL, inc.ID),
			Title:         fg.formatIncidentTitle(inc),
			ContentHTML:   fg.formatIncidentHTML(inc),
			ContentText:   fg.formatIncidentDescription(inc),
			Summary:       inc.Message,
			DatePublished: inc.CreatedAt.Format(time.RFC3339),
			DateModified:  inc.UpdatedAt.Format(time.RFC3339),
			Authors: []JSONAuthor{
				{Name: fg.author, URL: fg.baseURL},
			},
			Tags:     tags,
			Language: "en",
		}
		items = append(items, item)
	}

	feed := JSONFeed{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       fg.title + " - Status Updates",
		HomePageURL: fg.baseURL,
		FeedURL:     fg.baseURL + "/feed/json",
		Description: fg.description,
		UserComment: "This feed provides real-time status updates for " + fg.title + ". Subscribe to stay informed about incidents and maintenance.",
		Icon:        fg.baseURL + "/static/logo.svg",
		Favicon:     fg.baseURL + "/favicon.svg",
		Authors: []JSONAuthor{
			{Name: fg.author, URL: fg.baseURL},
		},
		Language: "en",
		Items:    items,
	}

	return json.MarshalIndent(feed, "", "  ")
}

// Helper functions for formatting

func (fg *FeedGenerator) formatIncidentTitle(inc storage.Incident) string {
	var icon string
	switch inc.Severity {
	case "critical":
		icon = "🔴"
	case "major":
		icon = "🟠"
	case "minor":
		icon = "🟡"
	default:
		icon = "ℹ️"
	}

	statusText := ""
	if inc.Status == "resolved" {
		statusText = " [Resolved]"
	}

	return fmt.Sprintf("%s %s%s", icon, inc.Title, statusText)
}

func (fg *FeedGenerator) formatIncidentDescription(inc storage.Incident) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Status: %s | Severity: %s\n",
		fg.mapStatusToLabel(inc.Status),
		fg.mapSeverityToLabel(inc.Severity)))

	if len(inc.AffectedServices) > 0 {
		sb.WriteString(fmt.Sprintf("Affected Services: %s\n", strings.Join(inc.AffectedServices, ", ")))
	}

	sb.WriteString(fmt.Sprintf("\n%s\n", inc.Message))

	if len(inc.Updates) > 0 {
		sb.WriteString("\n--- Timeline ---\n")
		for i := len(inc.Updates) - 1; i >= 0; i-- {
			u := inc.Updates[i]
			sb.WriteString(fmt.Sprintf("[%s] %s: %s\n",
				u.CreatedAt.Format("Jan 02, 15:04 MST"),
				fg.mapStatusToLabel(u.Status),
				u.Message))
		}
	}

	if inc.ResolvedAt != nil {
		sb.WriteString(fmt.Sprintf("\nResolved at: %s", inc.ResolvedAt.Format("Jan 02, 2006 15:04 MST")))
	}

	return sb.String()
}

func (fg *FeedGenerator) formatIncidentHTML(inc storage.Incident) string {
	var sb strings.Builder

	// Status badge
	badgeColor := fg.getSeverityColor(inc.Severity)
	statusBadge := fg.getStatusBadge(inc.Status)

	sb.WriteString(`<div style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 600px;">`)

	// Header with badges
	sb.WriteString(`<div style="margin-bottom: 16px;">`)
	sb.WriteString(fmt.Sprintf(`<span style="display: inline-block; padding: 4px 12px; border-radius: 4px; font-size: 12px; font-weight: 600; text-transform: uppercase; background-color: %s; color: white; margin-right: 8px;">%s</span>`,
		badgeColor, html.EscapeString(inc.Severity)))
	sb.WriteString(fmt.Sprintf(`<span style="display: inline-block; padding: 4px 12px; border-radius: 4px; font-size: 12px; font-weight: 600; text-transform: uppercase; background-color: %s; color: white;">%s</span>`,
		statusBadge, html.EscapeString(fg.mapStatusToLabel(inc.Status))))
	sb.WriteString(`</div>`)

	// Affected services
	if len(inc.AffectedServices) > 0 {
		sb.WriteString(`<div style="margin-bottom: 16px;"><strong>Affected Services:</strong> `)
		for i, svc := range inc.AffectedServices {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf(`<span style="background: #f1f5f9; padding: 2px 8px; border-radius: 4px; font-size: 13px;">%s</span>`, html.EscapeString(svc)))
		}
		sb.WriteString(`</div>`)
	}

	// Message
	sb.WriteString(fmt.Sprintf(`<div style="margin-bottom: 16px; padding: 16px; background: #f8fafc; border-radius: 8px; border-left: 4px solid %s;">%s</div>`,
		badgeColor, html.EscapeString(inc.Message)))

	// Timeline
	if len(inc.Updates) > 0 {
		sb.WriteString(`<div style="margin-top: 24px;"><h4 style="margin: 0 0 12px 0; font-size: 14px; text-transform: uppercase; letter-spacing: 0.5px; color: #64748b;">Timeline</h4>`)
		sb.WriteString(`<div style="border-left: 2px solid #e2e8f0; padding-left: 16px;">`)

		for i := len(inc.Updates) - 1; i >= 0; i-- {
			u := inc.Updates[i]
			sb.WriteString(fmt.Sprintf(`<div style="margin-bottom: 16px; position: relative;">
				<div style="position: absolute; left: -21px; top: 4px; width: 10px; height: 10px; border-radius: 50%%; background: %s;"></div>
				<div style="font-size: 12px; color: #64748b; margin-bottom: 4px;">%s</div>
				<div style="font-weight: 600; margin-bottom: 4px;">%s</div>
				<div style="color: #334155;">%s</div>
			</div>`,
				fg.getStatusBadge(u.Status),
				u.CreatedAt.Format("Jan 02, 2006 • 15:04 MST"),
				html.EscapeString(fg.mapStatusToLabel(u.Status)),
				html.EscapeString(u.Message)))
		}
		sb.WriteString(`</div></div>`)
	}

	// Resolution info
	if inc.ResolvedAt != nil {
		sb.WriteString(fmt.Sprintf(`<div style="margin-top: 16px; padding: 12px; background: #dcfce7; border-radius: 8px; color: #166534;">
			<strong>✓ Resolved:</strong> %s
		</div>`, inc.ResolvedAt.Format("January 02, 2006 at 15:04 MST")))
	}

	sb.WriteString(`</div>`)
	return sb.String()
}

func (fg *FeedGenerator) formatStatusTitle(status *StatusSummary) string {
	var icon string
	switch status.Overall {
	case "operational":
		icon = "✅"
	case "degraded":
		icon = "⚠️"
	case "down":
		icon = "🔴"
	default:
		icon = "ℹ️"
	}

	return fmt.Sprintf("%s Current Status: %s", icon, fg.mapOverallToLabel(status.Overall))
}

func (fg *FeedGenerator) formatStatusDescription(status *StatusSummary) string {
	return fmt.Sprintf("%s - %d/%d services operational, %d degraded, %d down",
		fg.mapOverallToLabel(status.Overall),
		status.Operational,
		status.Total,
		status.Degraded,
		status.Down)
}

func (fg *FeedGenerator) formatStatusSummary(status *StatusSummary) string {
	return fg.mapOverallToLabel(status.Overall)
}

func (fg *FeedGenerator) formatStatusHTML(status *StatusSummary) string {
	var sb strings.Builder
	var bgColor, textColor, barColor string

	switch status.Overall {
	case "operational":
		bgColor, textColor, barColor = "#dcfce7", "#166534", "#22c55e"
	case "degraded":
		bgColor, textColor, barColor = "#fef3c7", "#92400e", "#f59e0b"
	case "down":
		bgColor, textColor, barColor = "#fee2e2", "#991b1b", "#ef4444"
	default:
		bgColor, textColor, barColor = "#f1f5f9", "#475569", "#64748b"
	}

	sb.WriteString(`<div style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 600px;">`)

	// Status banner
	sb.WriteString(fmt.Sprintf(`<div style="padding: 20px; background: %s; border-radius: 12px; text-align: center; margin-bottom: 20px;">
		<div style="font-size: 24px; font-weight: 700; color: %s; margin-bottom: 4px;">%s</div>
		<div style="font-size: 14px; color: %s; opacity: 0.8;">Last updated: %s</div>
	</div>`,
		bgColor, textColor, fg.mapOverallToLabel(status.Overall), textColor, time.Now().Format("Jan 02, 2006 15:04 MST")))

	// Service stats
	sb.WriteString(`<div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; margin-bottom: 20px;">`)

	// Operational
	sb.WriteString(fmt.Sprintf(`<div style="text-align: center; padding: 16px; background: #f0fdf4; border-radius: 8px;">
		<div style="font-size: 28px; font-weight: 700; color: #166534;">%d</div>
		<div style="font-size: 12px; color: #166534; text-transform: uppercase;">Operational</div>
	</div>`, status.Operational))

	// Degraded
	sb.WriteString(fmt.Sprintf(`<div style="text-align: center; padding: 16px; background: #fffbeb; border-radius: 8px;">
		<div style="font-size: 28px; font-weight: 700; color: #92400e;">%d</div>
		<div style="font-size: 12px; color: #92400e; text-transform: uppercase;">Degraded</div>
	</div>`, status.Degraded))

	// Down
	sb.WriteString(fmt.Sprintf(`<div style="text-align: center; padding: 16px; background: #fef2f2; border-radius: 8px;">
		<div style="font-size: 28px; font-weight: 700; color: #991b1b;">%d</div>
		<div style="font-size: 12px; color: #991b1b; text-transform: uppercase;">Down</div>
	</div>`, status.Down))

	sb.WriteString(`</div>`)

	// Progress bar
	if status.Total > 0 {
		operationalPct := float64(status.Operational) / float64(status.Total) * 100
		sb.WriteString(fmt.Sprintf(`<div style="background: #e2e8f0; border-radius: 4px; height: 8px; overflow: hidden;">
			<div style="background: %s; height: 100%%; width: %.1f%%; transition: width 0.3s;"></div>
		</div>
		<div style="text-align: center; font-size: 13px; color: #64748b; margin-top: 8px;">
			%.1f%% of services operational
		</div>`, barColor, operationalPct, operationalPct))
	}

	sb.WriteString(`</div>`)
	return sb.String()
}

// Mapping helpers

func (fg *FeedGenerator) mapSeverityToCategory(severity string) string {
	switch severity {
	case "critical":
		return "Critical Incident"
	case "major":
		return "Major Incident"
	case "minor":
		return "Minor Incident"
	default:
		return "Incident"
	}
}

func (fg *FeedGenerator) mapSeverityToLabel(severity string) string {
	switch severity {
	case "critical":
		return "Critical"
	case "major":
		return "Major"
	case "minor":
		return "Minor"
	default:
		return severity
	}
}

func (fg *FeedGenerator) mapStatusToLabel(status string) string {
	switch status {
	case "investigating":
		return "Investigating"
	case "identified":
		return "Identified"
	case "monitoring":
		return "Monitoring"
	case "resolved":
		return "Resolved"
	default:
		return status
	}
}

func (fg *FeedGenerator) mapOverallToLabel(overall string) string {
	switch overall {
	case "operational":
		return "All Systems Operational"
	case "degraded":
		return "Partial System Outage"
	case "down":
		return "Major System Outage"
	default:
		return "Status Unknown"
	}
}

func (fg *FeedGenerator) getSeverityColor(severity string) string {
	switch severity {
	case "critical":
		return "#dc2626"
	case "major":
		return "#ea580c"
	case "minor":
		return "#ca8a04"
	default:
		return "#64748b"
	}
}

func (fg *FeedGenerator) getStatusBadge(status string) string {
	switch status {
	case "investigating":
		return "#ef4444"
	case "identified":
		return "#f97316"
	case "monitoring":
		return "#3b82f6"
	case "resolved":
		return "#22c55e"
	default:
		return "#64748b"
	}
}

// extractDomain extracts domain from URL for Atom tag URIs
func extractDomain(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	if idx := strings.Index(url, "/"); idx != -1 {
		url = url[:idx]
	}
	if idx := strings.Index(url, ":"); idx != -1 {
		url = url[:idx]
	}
	return url
}
