// Copyright 2013 - 2015 Sebastian Ruml <sebastian.ruml@gmail.com>

/*
Package osc provides a client and server for sending and receiving
OpenSoundControl messages.

The package is implemented in pure Go.

The implementation is based on the Open Sound Control 1.0 Specification
(http://opensoundcontrol.org/spec-1_0).

Open Sound Control (OSC) is an open, transport-independent, message-based
protocol developed for communication among computers, sound synthesizers,
and other multimedia devices.

Features:
  - Supports OSC messages with 'i' (Int32), 'f' (Float32),
    's' (string), 'b' (blob / binary data), 'h' (Int64), 't' (OSC timetag),
    'd' (Double/int64), 'T' (True), 'F' (False), 'N' (Nil) types.
  - OSC bundles, including timetags
  - Support for OSC address pattern including '*', '?', '{,}' and '[]' wildcards

This OSC implementation uses the UDP protocol for sending and receiving
OSC packets.

The unit of transmission of OSC is an OSC Packet. Any application that sends
OSC Packets is an OSC Client; any application that receives OSC Packets is
an OSC Server.

An OSC packet consists of its contents, a contiguous block of binary data,
and its size, the number of 8-bit bytes that comprise the contents. The
size of an OSC packet is always a multiple of 4.

OSC packets come in two flavors:

OSC Messages: An OSC message consists of an OSC address pattern, followed
by an OSC Type Tag String, and finally by zero or more OSC arguments.

OSC Bundles: An OSC Bundle consists of the string "#bundle" followed
by an OSC Time Tag, followed by zero or more OSC bundle elements. Each bundle
element can be another OSC bundle (note this recursive definition: bundle may
contain bundles) or OSC message.

An OSC bundle element consists of its size and its contents. The size is
an int32 representing the number of 8-bit bytes in the contents, and will
always be a multiple of 4. The contents are either an OSC Message or an
OSC Bundle.

The following argument types are supported: 'i' (Int32), 'f' (Float32),
's' (string), 'b' (blob / binary data), 'h' (Int64), 't' (OSC timetag),
'd' (Double/int64), 'T' (True), 'F' (False), 'N' (Nil).

go-osc supports the following OSC address patterns:
- '*', '?', '{,}' and '[]' wildcards.

# Usage

OSC client example:

	client := osc.NewClient("localhost", 8765)
	msg := osc.NewMessage("/osc/address")
	msg.Append(int32(111))
	msg.Append(true)
	msg.Append("hello")
	client.Send(msg)

OSC server example:

	addr := "127.0.0.1:8765"
	d := osc.NewStandardDispatcher()
	d.AddMsgHandler("/message/address", func(msg *osc.Message) {
	    fmt.Println(msg)
	})

	server := &osc.Server{
	    Addr: addr,
	    Dispatcher:d,
	}
	server.ListenAndServe()

OSC server and client example:

	done := sync.WaitGroup{}
	done.Add(1)

	addr1, _ := net.ResolveUDPAddr("udp", "127.0.0.1:8000")
	addr2, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9000")

	// OSC app 1 with AddMsgHandlerExt
	d1 := osc.NewStandardDispatcher()
	app1 := osc.NewServerAndClient(d1)
	app1.NewConn(addr2, nil)

	d1.AddMsgHandlerExt("*", func(msg *osc.Message, addr net.Addr) {
		fmt.Printf("%v -> %v: %v \n", addr, addr2, msg)
		app1.SendMsgTo(addr, "/pong", 2)
	})

	go app1.ListenAndServe()

	// OSC app 2 with AddMsgHandler
	d2 := osc.NewStandardDispatcher()
	d2.AddMsgHandler("*", func(msg *osc.Message) {
		fmt.Printf("-> %v: %v \n", addr1, msg)
		done.Done()
	})

	app2 := osc.NewServerAndClient(d2)
	app2.NewConn(addr1, addr2)

	go app2.ListenAndServe()

	app2.SendMsg("/ping", 1.0)

	done.Wait()
*/
package osc
