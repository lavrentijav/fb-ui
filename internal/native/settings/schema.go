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

	"notify.discord.enable": {Kind: KindBool, Default: "false", Section: "discord"},
	"notify.discord.url":    {Kind: KindSecret, Section: "discord", Check: CheckURL},
	"notify.discord.events": {Kind: KindList, Section: "discord"},
}
