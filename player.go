package main

import (
	"io"
	"sync"

	"github.com/diamondburned/arikawa/v3/voice"
	"github.com/diamondburned/arikawa/v3/voice/voicegateway"
	"github.com/jonas747/ogg"
)

// Player streams audio to a Discord voice session. All of its methods are safe to call concurrently.
type Player struct {
	v     *voice.Session
	src   *ogg.PacketDecoder
	pause chan bool
	stop  chan struct{}
	done  chan struct{}

	mu      sync.Mutex
	started bool
	paused  bool
	err     error
}

// NewPlayer returns a new player that streams ogg packets from src to v.
func NewPlayer(v *voice.Session, src *ogg.PacketDecoder) *Player {
	return &Player{v: v, src: src, done: make(chan struct{})}
}

func (p *Player) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return
	}
	p.started = true
	p.pause = make(chan bool)
	p.stop = make(chan struct{})
	go func() {
		err := p.run()
		p.mu.Lock()
		close(p.done)
		p.err = err
		p.mu.Unlock()
	}()
}

var opusSilence = []byte{
	0xF8, 0xFF, 0xFE,
	0xF8, 0xFF, 0xFE,
	0xF8, 0xFF, 0xFE,
	0xF8, 0xFF, 0xFE,
	0xF8, 0xFF, 0xFE,
}

func (p *Player) run() error {
	p.mu.Lock()
	paused := p.paused
	p.mu.Unlock()
	shutup := func() error {
		_, err := p.v.Write(opusSilence)
		if err != nil {
			return err
		}
		return p.v.Speaking(voicegateway.NotSpeaking)
	}
	for {
		select {
		case paused = <-p.pause:
		case <-p.stop:
			return shutup()
		default:
		}
		if paused {
			if err := shutup(); err != nil {
				return err
			}
			for paused {
				select {
				case paused = <-p.pause:
				case <-p.stop:
					return shutup()
				}
			}
		}

		data, _, err := p.src.Decode()
		if err == io.EOF {
			return shutup()
		} else if err != nil {
			shutup()
			return err
		}
		if _, err := p.v.Write(data); err != nil {
			return err
		}
	}
}

func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return
	}
	close(p.stop)
}

func (p *Player) Pause(pause bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		if p.paused == pause {
			return
		}
		p.pause <- pause
	}
	p.paused = pause
}

func (p *Player) Paused() bool {
	p.mu.Lock()
	defer p.mu.Lock()
	return p.paused
}

func (p *Player) Done() <-chan struct{} {
	return p.done
}

func (p *Player) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
