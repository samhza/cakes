package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/diamondburned/arikawa/v3/api"
	"github.com/diamondburned/arikawa/v3/discord"
	"github.com/diamondburned/arikawa/v3/gateway"
	"github.com/diamondburned/arikawa/v3/utils/bot"
	"github.com/diamondburned/arikawa/v3/utils/wsutil"
	"github.com/diamondburned/arikawa/v3/voice"
	"github.com/pelletier/go-toml/v2"
	"go.samhza.com/ytsearch"
)

type Config struct {
	Token    string
	Prefixes []string
}

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cfg Config
	if err := toml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	cfgpath := flag.String("config", "cakes.toml", "config file")
	flag.Parse()
	cfg, err := loadConfig(*cfgpath)
	if err != nil {
		log.Fatalln("loading config:", err)
	}
	b := new(Bot)
	b.queues = make(map[discord.GuildID]*Queue)
	b.Config = *cfg
	bot.Run(cfg.Token, b, func(c *bot.Context) error {
		wsutil.WSDebug = log.Println
		c.AddAliases("Play", "p")
		c.HasPrefix = bot.NewPrefix(cfg.Prefixes...)
		c.AddIntents(gateway.IntentGuildVoiceStates)
		c.ErrorReplier = func(err error, src *gateway.MessageCreateEvent) api.SendMessageData {
			return api.SendMessageData{
				Embeds: []discord.Embed{{
					Title:       "Error",
					Description: err.Error(),
					Color:       0xFF0000,
				}},
			}
		}
		return nil
	})
}

type Bot struct {
	Ctx    *bot.Context
	Config Config
	mu     sync.Mutex
	queues map[discord.GuildID]*Queue
}

func (b *Bot) Play(m *gateway.MessageCreateEvent, query bot.RawArguments) (string, error) {
	q, err := b.queue(m)
	if err != nil {
		return "", err
	}
	results, err := ytsearch.Search(string(query))
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", errors.New("no results")
	}
	q.Play(Song{
		Title: results[0].Title,
		ID:    results[0].ID,
	})
	return "Playing " + escape.Replace(results[0].Title), nil
}

func (b *Bot) Skip(m *gateway.MessageCreateEvent) (string, error) {
	q, err := b.queue(m)
	if err != nil {
		return "", err
	}
	q.Skip()
	return "Skipped", nil
}

func (b *Bot) Unpause(m *gateway.MessageCreateEvent) (string, error) {
	q, err := b.queue(m)
	if err != nil {
		return "", err
	}
	q.Pause(false)
	return "Unpaused", nil
}

func (b *Bot) Pause(m *gateway.MessageCreateEvent) (string, error) {
	q, err := b.queue(m)
	if err != nil {
		return "", err
	}
	q.Pause(true)
	return "Paused", nil
}

var escape = strings.NewReplacer(
	"@", "@\u200b",
	"`", "\\`",
	"*", "\\*",
	"_", "\\_",
	"~", "\\~",
	"\\", "\\\\",
)

func (b *Bot) queue(m *gateway.MessageCreateEvent) (*Queue, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	q, ok := b.queues[m.GuildID]
	if ok {
		return q, nil
	}
	vc, err := b.Ctx.VoiceState(m.GuildID, m.Author.ID)
	if err != nil {
		return nil, errors.New("couldn't get your voice channel: %w")
	}
	if vc.ChannelID == 0 {
		return nil, errors.New("not in a voice channel")
	}
	log.Println("MUSICBOT: creating new session")
	vs, err := voice.NewSession(b.Ctx.State)
	if err != nil {
		return nil, fmt.Errorf("creating voice session: %w", err)
	}
	log.Println("MUSICBOT: connecting to vc")
	if err = vs.JoinChannel(m.GuildID, vc.ChannelID, false, false); err != nil {
		return nil, fmt.Errorf("joining voice channel: %w", err)
	}
	q = NewQueue(vs)
	ctx, cancel := context.WithCancel(context.Background())
	vs.UseContext(ctx)
	b.queues[m.GuildID] = q
	q.Start()
	go func() {
		<-q.Done()
		b.mu.Lock()
		delete(b.queues, m.GuildID)
		b.mu.Unlock()
		vs.Leave()
		log.Println("MUSICBOT: session done")
		cancel()
	}()
	return q, nil
}
