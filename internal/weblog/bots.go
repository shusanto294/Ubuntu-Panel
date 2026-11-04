package weblog

import "strings"

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
