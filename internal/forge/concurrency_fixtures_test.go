package forge

import (
	"sync"
)

// race runs n copies of f started on one signal and returns their results.
func race(n int, f func(i int) int) []int {
	out := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i] = f(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return out
}

func count(codes []int, want int) int {
	n := 0
	for _, c := range codes {
		if c == want {
			n++
		}
	}
	return n
}
