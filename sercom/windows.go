package sercom

import (
	"os"
	"syscall"
	"github.com/knieriem/g/registry"
	win "github.com/knieriem/g/syscall"
)

const (
	initDefault = "r1 d1 b115200 l8 pn s1"
)

type hw struct {
	fd uint32

	initDone    bool
	dcb, dcbsav win.DCB

	ev struct {
		w win.Handle
		r win.Handle
	}
}

func Open(file string, inictl string) (p Port, err os.Error) {
	const (
		access     = syscall.GENERIC_READ | syscall.GENERIC_WRITE
		sharemode  = 0
		createmode = syscall.OPEN_EXISTING
		flags      = win.FILE_FLAG_OVERLAPPED
	)
	fd, e := syscall.CreateFile(syscall.StringToUTF16Ptr(file), access, sharemode, nil, createmode, flags, 0)
	if e != 0 {
		goto error
	}

	d := new(dev)
	d.fd = uint32(fd)
	d.name = file

	if err = d.Ctl(initDefault + " " + inictl); err != nil {
		return
	}
	d.initDone = true

	if d.ev.r, e = win.CreateEvent(win.EvManualReset, !win.EvInitiallyOn); e != 0 {
		goto error
	}
	if d.ev.w, e = win.CreateEvent(win.EvManualReset, !win.EvInitiallyOn); e != 0 {
		goto error
	}

	cto := win.CommTimeouts{
		//		ReadIntervalTimeout: ^uint32(0),
		//		ReadTotalTimeoutMultiplier: ^uint32(0),
		//		ReadTotalTimeoutConstant: ^uint32(0)-1,
		ReadIntervalTimeout: 10,
	}
	if e = win.SetCommTimeouts(d.fd, &cto); e != 0 {
		goto error
	}
	if e = win.SetupComm(d.fd, 4096, 4096); e != 0 {
		goto error
	}
	p = d
	return

error:
	err = &os.PathError{"open", file, os.Errno(e)}
	return
}

func (p *dev) Read(buf []byte) (int, os.Error) {
	var done uint32

	for {
		var ov syscall.Overlapped

		ov.HEvent = p.ev.r.Byteptr()
		if e := syscall.ReadFile(int32(p.fd), buf, &done, &ov); e != 0 {
			if e != syscall.ERROR_IO_PENDING {
			error:
				return 0, &os.PathError{"reading from", p.name, os.Errno(e)}
			}
			if e = win.GetOverlappedResult(p.fd, &ov, &done, 1); e != 0 {
				goto error
			}
		}
		if done > 0 {
			break
		}
	}
	return int(done), nil
}

func (p *dev) Write(buf []byte) (int, os.Error) {
	var done uint32

	for {
		var ov syscall.Overlapped

		ov.HEvent = p.ev.w.Byteptr()
		if e := syscall.WriteFile(int32(p.fd), buf, &done, &ov); e != 0 {
			if e != syscall.ERROR_IO_PENDING {
			error:
				return 0, &os.PathError{"writing to", p.name, os.Errno(e)}
			}
			if e = win.GetOverlappedResult(p.fd, &ov, &done, 1); e != 0 {
				goto error
			}
		}
		if done > 0 {
			break
		}
	}
	return int(done), nil
}

func (d *dev) Close() (err os.Error) {
	d.ev.r.Close()
	d.ev.w.Close()
	if e := syscall.CloseHandle(int32(d.fd)); e != 0 {
		err = d.errno("close", e)
	}
	return nil
}

func (d *dev) Drain() (err os.Error) {
	if e := win.FlushFileBuffers(d.fd); e != 0 {
		err = d.errno("drain", e)
	}
	return
}

func (d *dev) Purge(in, out bool) {
	// TBD
}


func (d *dev) SetBaudrate(val int) os.Error {
	d.dcb.BaudRate = uint32(val)
	return d.updateCtl()
}

func (d *dev) SetWordlen(n int) os.Error {
	switch n {
	case 5, 6, 7, 8:
		d.dcb.ByteSize = uint8(n)
	}
	return d.updateCtl()
}

func (d *dev) SetParity(val byte) os.Error {
	p := &d.dcb.Parity
	switch val {
	case 'o':
		*p = win.ODDPARITY
	case 'e':
		*p = win.EVENPARITY
	default:
		*p = win.NOPARITY
	}
	return d.updateCtl()
}

func (d *dev) SetStopbits(n int) os.Error {
	switch n {
	case 1:
		d.dcb.StopBits = win.ONESTOPBIT
	case 2:
		d.dcb.StopBits = win.TWOSTOPBITS
	default:
		return d.errorf("open", "invalid number of stopbits: %d", n)
	}
	return d.updateCtl()
}


func (d *dev) SetRts(on bool) (err os.Error) {
	d.rts = on
	setRtsFlags(&d.dcb, on)
	if !d.initDone {
		return
	}
	setRtsFlags(&d.dcbsav, on) // fake
	if on {
		return d.commfn("set rts", win.SETRTS)
	}
	return d.commfn("clr rts", win.CLRRTS)
}

func (d *dev) SetDtr(on bool) (err os.Error) {
	d.dtr = on
	setDtrFlags(&d.dcb, on)
	if !d.initDone {
		return
	}
	setDtrFlags(&d.dcbsav, on) // fake
	if on {
		return d.commfn("set dtr", win.SETDTR)
	}
	return d.commfn("clr dtr", win.CLRDTR)
}

func (d *dev) commfn(name string, f int) (err os.Error) {
	if e := win.EscapeCommFunction(d.fd, uint32(f)); e != 0 {
		return &os.PathError{name, d.name, os.Errno(e)}
	}
	return
}

func (d *dev) SetRtsCts(on bool) os.Error {
	dcb := &d.dcb

	if on {
		dcb.Flags &^= win.DCBmRtsControl << win.DCBpRtsControl
		dcb.Flags |= win.DCBfOutxCtsFlow
		dcb.Flags |= win.RTS_CONTROL_HANDSHAKE << win.DCBpRtsControl
	} else {
		dcb.Flags &^= win.DCBfOutxCtsFlow
		setRtsFlags(dcb, d.rts)
	}
	return d.updateCtl()
}

func setRtsFlags(dcb *win.DCB, on bool) {
	dcb.Flags &^= win.DCBmRtsControl << win.DCBpRtsControl
	if on {
		dcb.Flags |= win.RTS_CONTROL_ENABLE << win.DCBpRtsControl
	} else {
		dcb.Flags |= win.RTS_CONTROL_DISABLE << win.DCBpRtsControl
	}
}
func setDtrFlags(dcb *win.DCB, on bool) {
	dcb.Flags &^= win.DCBmDtrControl << win.DCBpDtrControl
	if on {
		dcb.Flags |= win.DTR_CONTROL_ENABLE << win.DCBpDtrControl
	} else {
		dcb.Flags |= win.DTR_CONTROL_DISABLE << win.DCBpDtrControl
	}
}


func (d *dev) updateCtl() (err os.Error) {
	if d.inCtl {
		return
	}
	sav := &d.dcbsav
	dcb := &d.dcb
	if dcb.Flags == sav.Flags && dcb.BaudRate == sav.BaudRate &&
		dcb.ByteSize == sav.ByteSize &&
		dcb.Parity == sav.Parity &&
		dcb.StopBits == sav.StopBits {
		return
	}
	if e := win.SetCommState(d.fd, &d.dcb); e != 0 {
		err = d.errno("setdcb", e)
	} else {
		d.dcbsav = d.dcb
	}
	return
}


func (d *dev) ModemLines() LineState {
	var ls LineState
	// TBD
	return ls
}



// Get a list of (probably) present serial devices
func DeviceList() (list []string) {
	key, err := registry.KeyLocalMachine.Subkey("HARDWARE", "DEVICEMAP", "SERIALCOMM")
	if err != nil {
		return
	}
	for _, v := range key.Values() {
		if s := v.String(); s != "" {
			list = append(list, s)
		}
	}
	return
}