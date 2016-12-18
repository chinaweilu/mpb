package mpb

import (
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/vbauerster/mpb/cwriter"
)

// ErrCallAfterStop thrown by panic, if Progress methods like AddBar() are called
// after Stop() has been called
var ErrCallAfterStop = errors.New("method call on stopped Progress instance")

type opType uint

const (
	opBarAdd opType = iota
	opBarRemove
)

type SortType uint

const (
	SortNone SortType = iota
	SortTop
	SortBottom
)

// default RefreshRate
const rr = 100

// Progress represents the container that renders Progress bars
type Progress struct {
	// WaitGroup for internal rendering sync
	wg *sync.WaitGroup

	out   io.Writer
	width int
	sort  SortType

	op             chan *operation
	rrChangeReqCh  chan time.Duration
	outChangeReqCh chan io.Writer
	countReqCh     chan chan int
	allDone        chan struct{}
}

type operation struct {
	kind   opType
	bar    *Bar
	result chan bool
}

// New returns a new progress bar with defaults
func New() *Progress {
	p := &Progress{
		width:          70,
		op:             make(chan *operation),
		rrChangeReqCh:  make(chan time.Duration),
		outChangeReqCh: make(chan io.Writer),
		countReqCh:     make(chan chan int),
		allDone:        make(chan struct{}),
		wg:             new(sync.WaitGroup),
	}
	go p.server(cwriter.New(os.Stdout), time.NewTicker(rr*time.Millisecond))
	return p
}

// SetWidth sets the width for all underlying bars
func (p *Progress) SetWidth(n int) *Progress {
	if n <= 0 {
		return p
	}
	p.width = n
	return p
}

// SetOut sets underlying writer of progress. Default is os.Stdout
// pancis, if called on stopped Progress instance, i.e after Stop()
func (p *Progress) SetOut(w io.Writer) *Progress {
	if p.isAllDone() {
		panic(ErrCallAfterStop)
	}
	if w == nil {
		return p
	}
	p.outChangeReqCh <- w
	return p
}

// RefreshRate overrides default (30ms) refreshRate value
// pancis, if called on stopped Progress instance, i.e after Stop()
func (p *Progress) RefreshRate(d time.Duration) *Progress {
	if p.isAllDone() {
		panic(ErrCallAfterStop)
	}
	p.rrChangeReqCh <- d
	return p
}

// WithSort sorts the bars, while redering
func (p *Progress) WithSort(sort SortType) *Progress {
	p.sort = sort
	return p
}

// AddBar creates a new progress bar and adds to the container
// pancis, if called on stopped Progress instance, i.e after Stop()
func (p *Progress) AddBar(total int) *Bar {
	if p.isAllDone() {
		panic(ErrCallAfterStop)
	}
	result := make(chan bool)
	bar := newBar(total, p.width, p.wg)
	p.op <- &operation{opBarAdd, bar, result}
	if <-result {
		p.wg.Add(1)
	}
	return bar
}

// RemoveBar removes bar at any time
// pancis, if called on stopped Progress instance, i.e after Stop()
func (p *Progress) RemoveBar(b *Bar) bool {
	if p.isAllDone() {
		panic(ErrCallAfterStop)
	}
	result := make(chan bool)
	p.op <- &operation{opBarRemove, b, result}
	return <-result
}

// BarsCount returns bars count in the container
// pancis, if called on stopped Progress instance, i.e after Stop()
func (p *Progress) BarsCount() int {
	if p.isAllDone() {
		panic(ErrCallAfterStop)
	}
	respCh := make(chan int)
	p.countReqCh <- respCh
	return <-respCh
}

// Stop waits for bars to finish rendering and stops the rendering goroutine
func (p *Progress) Stop() {
	if !p.isAllDone() {
		close(p.allDone)
		p.wg.Wait()
		close(p.op)
	}
}

// server monitors underlying channels and renders any progress bars
func (p *Progress) server(cw *cwriter.Writer, t *time.Ticker) {
	bars := make([]*Bar, 0, 4)
	for {
		select {
		case w := <-p.outChangeReqCh:
			cw.Flush()
			cw = cwriter.New(w)
		case op, ok := <-p.op:
			if !ok {
				t.Stop()
				for _, b := range bars {
					b.Stop()
				}
				return
			}
			switch op.kind {
			case opBarAdd:
				bars = append(bars, op.bar)
				op.result <- true
			case opBarRemove:
				var ok bool
				for i, b := range bars {
					if b == op.bar {
						bars = append(bars[:i], bars[i+1:]...)
						ok = true
						b.Stop()
						break
					}
				}
				op.result <- ok
			}
		case respCh := <-p.countReqCh:
			respCh <- len(bars)
		case <-t.C:
			width, _ := cwriter.TerminalWidth()
			switch p.sort {
			case SortTop:
				sort.Sort(sort.Reverse(SortableBarSlice(bars)))
			case SortBottom:
				sort.Sort(SortableBarSlice(bars))
			}
			for _, b := range bars {
				buf := b.Bytes(width)
				buf = append(buf, '\n')
				cw.Write(buf)
			}
			cw.Flush()
			for _, b := range bars {
				go func(b *Bar) {
					b.flushedCh <- struct{}{}
				}(b)
			}
		case d := <-p.rrChangeReqCh:
			t.Stop()
			t = time.NewTicker(d)
		}
	}
}

func (p *Progress) isAllDone() bool {
	select {
	case <-p.allDone:
		return true
	default:
		return false
	}
}
