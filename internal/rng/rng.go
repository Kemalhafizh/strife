// Package rng implements the deterministic random source used everywhere in
// Strife.
//
// We deliberately do not use math/rand: its algorithms and seeding rules are
// allowed to change between Go releases, and a seed written into an audit
// report must replay bit-for-bit forever. xoshiro256** seeded through
// SplitMix64 is small, fast, well studied and fully specified here.
package rng

import "hash/fnv"

// Rand is a xoshiro256** generator. It is not safe for concurrent use; give
// every goroutine or simulated actor its own stream via Fork.
type Rand struct {
	seed uint64
	s    [4]uint64
}

func splitmix64(x *uint64) uint64 {
	*x += 0x9e3779b97f4a7c15
	z := *x
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// New returns a generator seeded with seed.
func New(seed uint64) *Rand {
	r := &Rand{seed: seed}
	x := seed
	for i := range r.s {
		r.s[i] = splitmix64(&x)
	}
	return r
}

// Seed returns the seed this generator was created with.
func (r *Rand) Seed() uint64 { return r.seed }

func rotl(x uint64, k int) uint64 { return (x << k) | (x >> (64 - k)) }

// Uint64 returns the next 64 pseudo-random bits.
func (r *Rand) Uint64() uint64 {
	s := &r.s
	result := rotl(s[1]*5, 7) * 9
	t := s[1] << 17
	s[2] ^= s[0]
	s[3] ^= s[1]
	s[1] ^= s[2]
	s[0] ^= s[3]
	s[2] ^= t
	s[3] = rotl(s[3], 45)
	return result
}

// Fork derives an independent stream identified by label. The derived stream
// depends only on this generator's seed and the label, never on how many
// values have already been drawn, so adding a draw in one subsystem cannot
// perturb another subsystem's randomness.
func (r *Rand) Fork(label string) *Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(label))
	x := r.seed ^ h.Sum64()
	return New(splitmix64(&x))
}

// Uint64n returns a uniform value in [0, n). It panics if n == 0.
func (r *Rand) Uint64n(n uint64) uint64 {
	if n == 0 {
		panic("rng: Uint64n(0)")
	}
	// Rejection sampling removes modulo bias.
	limit := ^uint64(0) - (^uint64(0) % n)
	for {
		if v := r.Uint64(); v < limit {
			return v % n
		}
	}
}

// Intn returns a uniform value in [0, n). It panics if n <= 0.
func (r *Rand) Intn(n int) int {
	if n <= 0 {
		panic("rng: Intn with non-positive n")
	}
	return int(r.Uint64n(uint64(n)))
}

// Int64n returns a uniform value in [0, n). It panics if n <= 0.
func (r *Rand) Int64n(n int64) int64 {
	if n <= 0 {
		panic("rng: Int64n with non-positive n")
	}
	return int64(r.Uint64n(uint64(n)))
}

// Range returns a uniform value in [lo, hi). If hi <= lo it returns lo.
func (r *Rand) Range(lo, hi int64) int64 {
	if hi <= lo {
		return lo
	}
	return lo + r.Int64n(hi-lo)
}

// Float64 returns a uniform value in [0, 1).
func (r *Rand) Float64() float64 {
	return float64(r.Uint64()>>11) / (1 << 53)
}

// Bool returns true with probability p.
func (r *Rand) Bool(p float64) bool { return r.Float64() < p }

// Shuffle permutes n elements using swap (Fisher-Yates).
func (r *Rand) Shuffle(n int, swap func(i, j int)) {
	for i := n - 1; i > 0; i-- {
		swap(i, r.Intn(i+1))
	}
}

// Perm returns a random permutation of [0, n).
func (r *Rand) Perm(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	r.Shuffle(n, func(i, j int) { p[i], p[j] = p[j], p[i] })
	return p
}
