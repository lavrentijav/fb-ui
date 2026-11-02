package settings

// Fleet is the master's settings schema: the keys the interface and the
// notification transports read. It is deliberately small; a key is added when
// a module needs it, never speculatively.
var Fleet = Schema{
	"panel.name": {Kind: KindString, Default: "", Section: "general"},

	"notify.telegram.enable": {Kind: KindBool, Default: "false", Section: "telegram"},
	"notify.telegram.token":  {Kind: KindSecret, Section: "telegram", Check: CheckTelegramToken},
	"notify.telegram.chats":  {Kind: KindList, Section: "telegram"},
	"notify.telegram.events": {Kind: KindList, Section: "telegram"},

	"notify.email.enable": {Kind: KindBool, Default: "false", Section: "email"},
	"notify.email.host":   {Kind: KindString, Section: "email"},
	"notify.email.port":   {Kind: KindInt, Default: "587", Min: 1, Section: "email"},
	"notify.email.tls":    {Kind: KindBool, Default: "false", Section: "email"}, // implicit TLS (465); otherwise STARTTLS
	"notify.email.user":   {Kind: KindString, Section: "email"},
	"notify.email.pass":   {Kind: KindSecret, Section: "email"},
	"notify.email.from":   {Kind: KindString, Section: "email"},
	"notify.email.to":     {Kind: KindList, Section: "email"},
	"notify.email.events": {Kind: KindList, Section: "email"},

	// The subscription response: what a client shows besides the links. All of
	// it is optional; an empty value is simply not sent. Keys named after the
	// de-facto headers they fill (see docs/design/native-sub-params.md).
	"sub.title":              {Kind: KindString, Section: "subscription"},
	"sub.updateHours":        {Kind: KindInt, Default: "12", Min: 0, Section: "subscription"},
	"sub.supportURL":         {Kind: KindString, Section: "subscription", Check: CheckURL},
	"sub.webPageURL":         {Kind: KindString, Section: "subscription", Check: CheckURL},
	"sub.announce":           {Kind: KindString, Section: "subscription"},
	"sub.announceURL":        {Kind: KindString, Section: "subscription", Check: CheckURL},
	"sub.announceColor":      {Kind: KindString, Default: "blue", Section: "subscription"},
	"sub.announceButtonText": {Kind: KindString, Section: "subscription"},
	"sub.announceButtonLink": {Kind: KindString, Section: "subscription", Check: CheckURL},
	"sub.providerID":         {Kind: KindString, Section: "subscription"},
	"sub.refillDate":         {Kind: KindString, Section: "subscription", Check: CheckDate},
	"sub.backupURLs":         {Kind: KindList, Section: "subscription"},
	"sub.remarkTemplate":     {Kind: KindString, Default: "{{NODE}}-{{INBOUND}}", Section: "subscription"},
	"sub.showIdentity":       {Kind: KindBool, Default: "false", Section: "subscription"},

	"notify.discord.enable": {Kind: KindBool, Default: "false", Section: "discord"},
	"notify.discord.url":    {Kind: KindSecret, Section: "discord", Check: CheckURL},
	"notify.discord.events": {Kind: KindList, Section: "discord"},
}
