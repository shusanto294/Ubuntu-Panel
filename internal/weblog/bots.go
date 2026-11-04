package weblog

import (
	"context"
	"net"
	"strings"
	"time"
)

// Useful bots: the crawlers that put the sites in search engines and AI
// answers, the previews a link gets when it is shared, and the monitors
// checking the sites are up. They make a lot of requests, follow links to
// pages that are gone, and come back often — everything that makes an
// ordinary address look like a crawler for files or a flood. None of that is
// held against them.
//
// What is still held against them is what no real crawler does: guessing
// passwords, posting to XML-RPC, asking for .env. A "Googlebot" doing that is
// somebody else using its name. A user agent is only a claim; the panel checks
// the big search engines' by their reverse DNS where it shows an address
// (see web/bots.go).

// GoodBot is a known useful bot.
type GoodBot struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // search | ai | preview | monitor

	// Domains are what a real one's reverse DNS ends in, for the ones whose
	// owners publish that; empty where they do not.
	Domains []string `json:"-"`
}

var goodBots = []struct {
	marker string // lower case, matched anywhere in the user agent
	bot    GoodBot
}{
	// Search engines.
	{"googlebot", GoodBot{"Googlebot", "search", []string{".googlebot.com", ".google.com", ".googleusercontent.com"}}},
	{"google-inspectiontool", GoodBot{"Google Search Console", "search", []string{".googlebot.com", ".google.com"}}},
	{"googleother", GoodBot{"Google", "search", []string{".googlebot.com", ".google.com"}}},
	{"adsbot-google", GoodBot{"Google Ads", "search", []string{".googlebot.com", ".google.com"}}},
	{"mediapartners-google", GoodBot{"Google AdSense", "search", []string{".googlebot.com", ".google.com"}}},
	{"storebot-google", GoodBot{"Google Shopping", "search", []string{".googlebot.com", ".google.com"}}},
	{"bingbot", GoodBot{"Bingbot", "search", []string{".search.msn.com"}}},
	{"bingpreview", GoodBot{"Bing", "search", []string{".search.msn.com"}}},
	{"msnbot", GoodBot{"Bing", "search", []string{".search.msn.com"}}},
	{"adidxbot", GoodBot{"Bing Ads", "search", []string{".search.msn.com"}}},
	{"applebot", GoodBot{"Applebot", "search", []string{".applebot.apple.com"}}},
	{"duckduckbot", GoodBot{"DuckDuckGo", "search", nil}},
	{"duckassistbot", GoodBot{"DuckDuckGo", "search", nil}},
	{"yandex", GoodBot{"Yandex", "search", []string{".yandex.ru", ".yandex.net", ".yandex.com"}}},
	{"baiduspider", GoodBot{"Baidu", "search", []string{".baidu.com", ".baidu.jp"}}},
	{"seznambot", GoodBot{"Seznam", "search", []string{".seznam.cz"}}},
	{"yeti/", GoodBot{"Naver", "search", []string{".naver.com"}}},
	{"petalbot", GoodBot{"Petal Search", "search", []string{".petalsearch.com", ".aspiegel.com"}}},
	{"qwantify", GoodBot{"Qwant", "search", nil}},
	{"ecosia", GoodBot{"Ecosia", "search", nil}},
	{"feedfetcher-google", GoodBot{"Google feeds", "search", []string{".googlebot.com", ".google.com"}}},
	{"google-read-aloud", GoodBot{"Google Read Aloud", "search", []string{".google.com", ".googleusercontent.com"}}},
	{"google-site-verification", GoodBot{"Google Search Console", "search", []string{".google.com"}}},
	{"apis-google", GoodBot{"Google", "search", []string{".googlebot.com", ".google.com"}}},
	{"slurp", GoodBot{"Yahoo", "search", []string{".crawl.yahoo.net"}}},
	{"mojeekbot", GoodBot{"Mojeek", "search", nil}},
	{"sogou", GoodBot{"Sogou", "search", nil}},
	{"coccocbot", GoodBot{"Cốc Cốc", "search", nil}},
	{"ahrefsbot", GoodBot{"Ahrefs", "search", nil}},
	{"semrushbot", GoodBot{"Semrush", "search", nil}},
	{"archive.org_bot", GoodBot{"Internet Archive", "search", nil}},
	{"ia_archiver", GoodBot{"Internet Archive", "search", nil}},

	// AI assistants and their crawlers.
	{"gptbot", GoodBot{"OpenAI GPTBot", "ai", nil}},
	{"chatgpt-user", GoodBot{"ChatGPT", "ai", nil}},
	{"oai-searchbot", GoodBot{"OpenAI search", "ai", nil}},
	{"claudebot", GoodBot{"Anthropic ClaudeBot", "ai", nil}},
	{"claude-user", GoodBot{"Claude", "ai", nil}},
	{"claude-searchbot", GoodBot{"Claude search", "ai", nil}},
	{"anthropic-ai", GoodBot{"Anthropic", "ai", nil}},
	{"perplexitybot", GoodBot{"Perplexity", "ai", nil}},
	{"perplexity-user", GoodBot{"Perplexity", "ai", nil}},
	{"applebot-extended", GoodBot{"Apple Intelligence", "ai", []string{".applebot.apple.com"}}},
	{"amazonbot", GoodBot{"Amazonbot", "ai", []string{".crawl.amazonbot.amazon"}}},
	{"meta-externalagent", GoodBot{"Meta AI", "ai", nil}},
	{"meta-externalfetcher", GoodBot{"Meta AI", "ai", nil}},
	{"mistralai-user", GoodBot{"Mistral", "ai", nil}},
	{"cohere-ai", GoodBot{"Cohere", "ai", nil}},
	{"youbot", GoodBot{"You.com", "ai", nil}},
	{"ccbot", GoodBot{"Common Crawl", "ai", nil}},
	{"duckassist", GoodBot{"DuckDuckGo AI", "ai", nil}},
	{"google-cloudvertexbot", GoodBot{"Google Vertex AI", "ai", []string{".google.com", ".googlebot.com"}}},
	{"gemini-deep-research", GoodBot{"Google Gemini", "ai", []string{".google.com", ".googlebot.com"}}},
	{"mistralai", GoodBot{"Mistral", "ai", nil}},
	{"ai2bot", GoodBot{"Ai2", "ai", nil}},
	{"diffbot", GoodBot{"Diffbot", "ai", nil}},

	// Link previews.
	{"facebookexternalhit", GoodBot{"Facebook preview", "preview", nil}},
	{"facebot", GoodBot{"Facebook", "preview", nil}},
	{"twitterbot", GoodBot{"X (Twitter) preview", "preview", nil}},
	{"linkedinbot", GoodBot{"LinkedIn preview", "preview", nil}},
	{"slackbot", GoodBot{"Slack preview", "preview", nil}},
	{"discordbot", GoodBot{"Discord preview", "preview", nil}},
	{"telegrambot", GoodBot{"Telegram preview", "preview", nil}},
	{"whatsapp", GoodBot{"WhatsApp preview", "preview", nil}},
	{"pinterestbot", GoodBot{"Pinterest", "preview", nil}},
	{"redditbot", GoodBot{"Reddit preview", "preview", nil}},
	{"skypeuripreview", GoodBot{"Skype preview", "preview", nil}},

	// Uptime monitors.
	{"uptimerobot", GoodBot{"UptimeRobot", "monitor", nil}},
	{"pingdom", GoodBot{"Pingdom", "monitor", nil}},
	{"statuscake", GoodBot{"StatusCake", "monitor", nil}},
	{"betteruptime", GoodBot{"Better Stack", "monitor", nil}},
	{"better stack", GoodBot{"Better Stack", "monitor", nil}},
	{"site24x7", GoodBot{"Site24x7", "monitor", nil}},
	{"freshping", GoodBot{"Freshping", "monitor", nil}},
	{"hetrixtools", GoodBot{"HetrixTools", "monitor", nil}},
	{"jetpack by wordpress.com", GoodBot{"Jetpack", "monitor", nil}},
}

// FindGoodBot is the useful bot a user agent says it is, or nil.
func FindGoodBot(agent string) *GoodBot {
	if agent == "" {
		return nil
	}

	lower := strings.ToLower(agent)

	// The most specific first: "applebot-extended" before "applebot".
	var found *GoodBot
	longest := 0
	for i := range goodBots {
		if strings.Contains(lower, goodBots[i].marker) && len(goodBots[i].marker) > longest {
			found, longest = &goodBots[i].bot, len(goodBots[i].marker)
		}
	}

	return found
}

// GoodBotMarkers are what a useful bot's user agent contains, lower case, for
// the protections that let them through by user agent.
func GoodBotMarkers() []string {
	markers := make([]string, 0, len(goodBots))
	for _, bot := range goodBots {
		markers = append(markers, bot.marker)
	}
	return markers
}

// CrawlerDomains are every domain a verifiable crawler's reverse DNS ends in.
func CrawlerDomains() []string {
	seen := map[string]bool{}
	domains := []string{}
	for _, bot := range goodBots {
		for _, domain := range bot.bot.Domains {
			if !seen[domain] {
				seen[domain] = true
				domains = append(domains, domain)
			}
		}
	}
	return domains
}

// CheckDNS is whether an address is a crawler of one of domains: its reverse
// DNS ends in one, and that name resolves back to the address. Forward-
// confirmed, because anybody can set a reverse record saying googlebot.com;
// nobody but Google can make that name resolve back. host is the reverse name
// found, verified or not.
func CheckDNS(ctx context.Context, ip string, domains []string) (host string, verified bool) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return "", false
	}

	for _, name := range names {
		candidate := strings.ToLower(strings.TrimSuffix(name, "."))

		matched := false
		for _, domain := range domains {
			if strings.HasSuffix(candidate, domain) {
				matched = true
			}
		}
		if !matched {
			continue
		}

		addresses, err := net.DefaultResolver.LookupHost(ctx, candidate)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if net.ParseIP(address).Equal(net.ParseIP(ip)) {
				return candidate, true
			}
		}
	}

	return strings.TrimSuffix(names[0], "."), false
}
