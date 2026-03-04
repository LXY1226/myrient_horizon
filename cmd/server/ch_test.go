package main

import (
	"testing"
	"time"
)

func TestChan(t *testing.T) {
	ch := make(chan int, 1)
	replacing := make(chan bool)
	go func() {
		for {
			select {
			case i := <-ch:
				t.Log("received", i)
				time.Sleep(1 * time.Second)
			case <-replacing:
				t.Log("replacing")
			}
		}
	}()
	time.Sleep(1 * time.Second)

	ch = make(chan int, 2)
	t.Log("send", 1)
	ch <- 1
	t.Log("send", 2)
	ch <- 2
	t.Log("send", 3)
	ch <- 3
	ch = make(chan int, 2)
	t.Log("send", 1)
	ch <- 1
	select {}
}
