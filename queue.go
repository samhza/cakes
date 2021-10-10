package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/diamondburned/arikawa/v3/voice"
	"github.com/diamondburned/arikawa/v3/voice/voicegateway"
	"github.com/jonas747/ogg"
	"github.com/kkdai/youtube/v2"
)

type Queue struct {
	mu      sync.Mutex
	pl      *Player
	moar    chan Song
	skip    chan<- struct{}
	started bool
	done    chan struct{}
	v       *voice.Session
	Entries []Song
}

func (q *Queue) Start() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started {
		return
	}
	q.started = true
	skip := make(chan struct{})
	pause := make(chan bool)
	q.skip = skip
	go func() {
		q.run(skip, pause)
		close(q.done)
	}()
}

func (q *Queue) Done() <-chan struct{} {
	return q.done
}

func (q *Queue) Play(s Song) {
	q.mu.Lock()
	if q.moar == nil {
		q.Entries = append(q.Entries, s)
		q.mu.Unlock()
		return
	}
	moar := q.moar
	q.moar = nil
	q.mu.Unlock()
	moar <- s
}

func (q *Queue) Skip() {
	q.skip <- struct{}{}
}

func (q *Queue) Pause(p bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pl != nil {
		q.pl.Pause(p)
	}
}

func (q *Queue) Playing() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pl == nil {
		return false
	}
	return !q.pl.Paused()
}

func (q *Queue) Paused() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pl == nil {
		return false
	}
	return q.pl.Paused()
}

func (q *Queue) run(skip <-chan struct{}, pause <-chan bool) {
	var src io.Reader
	var endstream func() error
	var pldone <-chan struct{}
	var nextsong <-chan Song
	var timer *time.Timer
	var idlech <-chan time.Time
	for {
		if q.pl == nil {
			nextsong = q.nextSong()
			timer = time.NewTimer(5 * time.Minute)
			idlech = timer.C
		}
		select {
		case song := <-nextsong:
			timer.Stop()
			nextsong = nil
			var err error
			src, endstream, err = stream(song.ID)
			if err != nil {
				log.Println("fetching stream:", err)
			}
			q.mu.Lock()
			q.pl = NewPlayer(q.v, ogg.NewPacketDecoder(ogg.NewDecoder(src)))
			q.pl.Start()
			pldone = q.pl.Done()
			q.mu.Unlock()
			err = q.v.Speaking(voicegateway.Microphone)
			if err != nil {
				log.Println("starting speaking:", err)
			}
		case <-idlech:
			return
		case <-pldone:
			q.mu.Lock()
			err := q.pl.Err()
			if err != nil {
				log.Println("player error:", err)
			}
			q.pl = nil
			pldone = nil
			q.mu.Unlock()
		case <-skip:
			q.mu.Lock()
			if q.pl != nil {
				q.pl.Stop()
				endstream()
				io.Copy(io.Discard, src)
			}
			q.mu.Unlock()
		}
	}
}

func (q *Queue) nextSong() <-chan Song {
	q.mu.Lock()
	if len(q.Entries) > 0 {
		ch := make(chan Song)
		entries := q.Entries
		q.Entries = make([]Song, len(q.Entries)-1)
		copy(q.Entries, entries[1:])
		go func() {
			ch <- entries[0]
		}()
		return ch
	}
	moar := make(chan Song)
	q.moar = moar
	q.mu.Unlock()
	return moar

}

func NewQueue(v *voice.Session) *Queue {
	return &Queue{
		done: make(chan struct{}),
		v:    v,
	}
}

type Song struct {
	Title, ID string
}

func stream(id string) (io.Reader, func() error, error) {
	ytc := new(youtube.Client)
	vid, err := ytc.GetVideo(id)
	if err != nil {
		return nil, nil, fmt.Errorf("getting video: %w", err)
	}
	var format *youtube.Format
Outer:
	for _, fmt := range vid.Formats {
		switch fmt.ItagNo {
		case 249, 250, 251:
			format = &fmt
			break Outer
		}
	}
	if format == nil {
		return nil, nil, errors.New("no suitable format found")
	}
	url, err := ytc.GetStreamURL(vid, format)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching stream URL: %w", err)
	}
	cmd := exec.Command("ffmpeg", "-i", url, "-c:a", "copy", "-vn", "-f", "ogg", "-")
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("getting stdout pipe: %w", err)
	}
	stop := func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	return r, stop, cmd.Start()
}
