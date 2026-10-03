package settings

import (
	stdhtml "html"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
)

type notificationMessage struct {
	Content string
	Format  NotificationTemplateFormat
}

func normalizeNotificationTemplateFormat(format NotificationTemplateFormat) NotificationTemplateFormat {
	switch format {
	case NotificationTemplateFormatMarkdown, NotificationTemplateFormatHTML:
		return format
	default:
		return NotificationTemplateFormatText
	}
}

// telegramHTMLForChannel preserves the documented Telegram HTML subset and discards unsafe
// elements and attributes. This keeps arbitrary admin-authored HTML from turning into an invalid
// Bot API request while still retaining headings, emphasis, links, quotes, lists, and code.
func telegramHTMLForChannel(source string) string {
	document, err := xhtml.Parse(strings.NewReader(source))
	if err != nil {
		return stdhtml.EscapeString(source)
	}
	var builder strings.Builder
	renderTelegramHTMLNode(&builder, document)
	return strings.TrimSpace(normalizeBlockWhitespace(builder.String()))
}

func renderTelegramHTMLNode(builder *strings.Builder, node *xhtml.Node) {
	if node.Type == xhtml.TextNode {
		builder.WriteString(stdhtml.EscapeString(node.Data))
		return
	}
	if node.Type != xhtml.ElementNode && node.Type != xhtml.DocumentNode {
		return
	}

	tag := strings.ToLower(node.Data)
	switch tag {
	case "script", "style", "noscript", "template", "head", "iframe", "object":
		return
	case "br":
		builder.WriteByte('\n')
		return
	case "img":
		builder.WriteString(stdhtml.EscapeString(strings.TrimSpace(nodeAttribute(node, "alt"))))
		return
	case "li":
		ensureLineBreaks(builder, 1)
		builder.WriteString("- ")
		renderTelegramHTMLChildren(builder, node)
		ensureLineBreaks(builder, 1)
		return
	case "a":
		href := safeLink(nodeAttribute(node, "href"))
		if href != "" {
			builder.WriteString(`<a href="`)
			builder.WriteString(stdhtml.EscapeString(href))
			builder.WriteString(`">`)
			renderTelegramHTMLChildren(builder, node)
			builder.WriteString("</a>")
		} else {
			renderTelegramHTMLChildren(builder, node)
		}
		return
	}

	openTag, closeTag := "", ""
	switch tag {
	case "strong", "b", "h1", "h2", "h3", "h4", "h5", "h6":
		openTag, closeTag = "<b>", "</b>"
	case "em", "i":
		openTag, closeTag = "<i>", "</i>"
	case "u", "ins":
		openTag, closeTag = "<u>", "</u>"
	case "del", "s", "strike":
		openTag, closeTag = "<s>", "</s>"
	case "code":
		openTag, closeTag = "<code>", "</code>"
	case "pre":
		openTag, closeTag = "<pre>", "</pre>"
	case "blockquote":
		openTag, closeTag = "<blockquote>", "</blockquote>"
	case "p", "div", "section", "article", "header", "footer", "ul", "ol":
		ensureLineBreaks(builder, 2)
		closeTag = "\n\n"
	}

	builder.WriteString(openTag)
	renderTelegramHTMLChildren(builder, node)
	builder.WriteString(closeTag)
}

func renderTelegramHTMLChildren(builder *strings.Builder, node *xhtml.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		renderTelegramHTMLNode(builder, child)
	}
}

func nodeAttribute(node *xhtml.Node, name string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return strings.TrimSpace(attribute.Val)
		}
	}
	return ""
}

func safeLink(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto", "tg":
		return parsed.String()
	default:
		return ""
	}
}

func ensureLineBreaks(builder *strings.Builder, count int) {
	value := builder.String()
	existing := 0
	for index := len(value) - 1; index >= 0 && value[index] == '\n'; index-- {
		existing++
	}
	if existing < count {
		builder.WriteString(strings.Repeat("\n", count-existing))
	}
}

func normalizeBlockWhitespace(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	for strings.Contains(value, "\n\n\n") {
		value = strings.ReplaceAll(value, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(value)
}
