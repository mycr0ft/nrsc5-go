package nrsc5

// Reed-Solomon decoder, ported from Phil Karn's rs_init.c / rs_decode.c
// (GPL), as used by nrsc5 with parameters (8, 0x11d, 1, 1, 8).

const rsA0 = 255 // A0: log(0) = -inf sentinel (== NN for mm=8)

// rsState holds the Galois field tables and generator polynomial.
type rsState struct {
	mm      int    // bits per symbol
	nn      int    // symbols per block ((1<<mm)-1)
	alphaTo []byte // antilog lookup table (index form -> poly form)
	indexOf []byte // log lookup table (poly form -> index form)
	genpoly []byte // generator polynomial (index form)
	nroots  int    // number of parity symbols
	fcr     int    // first consecutive root, index form
	prim    int    // primitive element, index form
	iprim   int    // prim-th root of 1, index form
}

// rsModnn computes x modulo nn using the shift trick from rs_char.h.
func rsModnn(rs *rsState, x uint) uint {
	for x >= uint(rs.nn) {
		x -= uint(rs.nn)
		x = (x >> uint(rs.mm)) + (x & uint(rs.nn))
	}
	return x
}

// initRs initializes a Reed-Solomon codec.
//
//	symsize = symbol size in bits (1-8)
//	gfpoly  = field generator polynomial coefficients
//	fcr     = first root of RS generator polynomial, index form
//	prim    = primitive element to generate polynomial roots, index form
//	nroots  = degree of RS generator polynomial (number of parity symbols)
func initRs(symsize, gfpoly, fcr, prim, nroots uint) *rsState {
	if symsize > 8 {
		return nil
	}
	if fcr >= 1<<symsize {
		return nil
	}
	if prim == 0 || prim >= 1<<symsize {
		return nil
	}
	if nroots >= 1<<symsize {
		return nil
	}

	rs := &rsState{
		mm:     int(symsize),
		nn:     (1 << symsize) - 1,
		nroots: int(nroots),
		fcr:    int(fcr),
		prim:   int(prim),
	}
	rs.alphaTo = make([]byte, rs.nn+1)
	rs.indexOf = make([]byte, rs.nn+1)

	// Generate Galois field lookup tables.
	rs.indexOf[0] = rsA0 // log(zero) = -inf
	rs.alphaTo[rsA0] = 0 // alpha**-inf = 0
	sr := uint(1)
	for i := uint(0); i < uint(rs.nn); i++ {
		rs.indexOf[sr] = byte(i)
		rs.alphaTo[i] = byte(sr)
		sr <<= 1
		if sr&(1<<symsize) != 0 {
			sr ^= gfpoly
		}
		sr &= uint(rs.nn)
	}
	if sr != 1 {
		// field generator polynomial is not primitive
		return nil
	}

	// Find prim-th root of 1, used in decoding.
	var iprim int
	for iprim = 1; iprim%rs.prim != 0; iprim += rs.nn {
	}
	rs.iprim = iprim / rs.prim

	// Form RS code generator polynomial from its roots.
	rs.genpoly = make([]byte, rs.nroots+1)
	rs.genpoly[0] = 1
	root := fcr * prim
	for i := uint(0); i < nroots; i, root = i+1, root+prim {
		rs.genpoly[i+1] = 1

		// Multiply genpoly[] by alpha**(root + x)
		for j := i; j > 0; j-- {
			if rs.genpoly[j] != 0 {
				rs.genpoly[j] = rs.genpoly[j-1] ^
					rs.alphaTo[rsModnn(rs, uint(rs.indexOf[rs.genpoly[j]])+root)]
			} else {
				rs.genpoly[j] = rs.genpoly[j-1]
			}
		}
		// genpoly[0] can never be zero
		rs.genpoly[0] = rs.alphaTo[rsModnn(rs, uint(rs.indexOf[rs.genpoly[0]])+root)]
	}
	// Convert genpoly[] to index form.
	for i := 0; i <= rs.nroots; i++ {
		rs.genpoly[i] = rs.indexOf[rs.genpoly[i]]
	}

	return rs
}

// decodeRs decodes a Reed-Solomon codeword in place. data holds NN symbols
// (for shortened codes, the leading positions must be zero). erasPos holds
// erasure positions (may be nil if noEras == 0). Returns the number of
// corrected symbols, or -1 if the codeword is uncorrectable.
func decodeRs(rs *rsState, data []byte, erasPos []int, noEras int) int {
	var (
		degLambda, el, degOmega int
		i, j, r, k              int
		u, q, tmp, num1, num2   uint
		den, discrR             byte
	)
	nroots := rs.nroots
	nn := rs.nn
	lambda := make([]byte, nroots+1)
	s := make([]byte, nroots)
	b := make([]byte, nroots+1)
	t := make([]byte, nroots+1)
	omega := make([]byte, nroots+1)
	root := make([]uint, nroots)
	reg := make([]byte, nroots+1)
	loc := make([]int, nroots)

	// Form the syndromes; i.e., evaluate data(x) at roots of g(x).
	for i = 0; i < nroots; i++ {
		s[i] = data[0]
	}
	for j = 1; j < nn; j++ {
		for i = 0; i < nroots; i++ {
			if s[i] == 0 {
				s[i] = data[j]
			} else {
				s[i] = data[j] ^ rs.alphaTo[rsModnn(rs, uint(rs.indexOf[s[i]])+uint(rs.fcr+i)*uint(rs.prim))]
			}
		}
	}

	// Convert syndromes to index form, checking for nonzero condition.
	synError := 0
	for i = 0; i < nroots; i++ {
		synError |= int(s[i])
		s[i] = rs.indexOf[s[i]]
	}

	if synError == 0 {
		// Syndromes are zero: data[] is a codeword with no errors.
		return 0
	}

	lambda[0] = 1
	for i := 1; i <= nroots; i++ {
		lambda[i] = 0
	}

	if noEras > 0 {
		// Init lambda to be the erasure locator polynomial.
		lambda[1] = rs.alphaTo[rsModnn(rs, uint(rs.prim*(nn-1-erasPos[0])))]
		for i = 1; i < noEras; i++ {
			u = rsModnn(rs, uint(rs.prim*(nn-1-erasPos[i])))
			for j = i + 1; j > 0; j-- {
				tmp = uint(rs.indexOf[lambda[j-1]])
				if tmp != rsA0 {
					lambda[j] ^= rs.alphaTo[rsModnn(rs, u+tmp)]
				}
			}
		}
	}
	for i = 0; i <= nroots; i++ {
		b[i] = rs.indexOf[lambda[i]]
	}

	// Berlekamp-Massey algorithm to determine error+erasure locator poly.
	r = noEras
	el = noEras
	for {
		r++
		if uint(r) > uint(nroots) {
			break
		}
		// Compute discrepancy at the r-th step in poly form.
		discrR = 0
		for i = 0; i < r; i++ {
			if lambda[i] != 0 && s[r-i-1] != rsA0 {
				discrR ^= rs.alphaTo[rsModnn(rs, uint(rs.indexOf[lambda[i]])+uint(s[r-i-1]))]
			}
		}
		discrR = rs.indexOf[discrR] // index form
		if discrR == rsA0 {
			// B(x) <-- x*B(x)
			copy(b[1:], b[:nroots])
			b[0] = rsA0
		} else {
			// T(x) <-- lambda(x) - discr_r*x*b(x)
			t[0] = lambda[0]
			for i = 0; i < nroots; i++ {
				if b[i] != rsA0 {
					t[i+1] = lambda[i+1] ^ rs.alphaTo[rsModnn(rs, uint(discrR)+uint(b[i]))]
				} else {
					t[i+1] = lambda[i+1]
				}
			}
			if 2*el <= r+noEras-1 {
				el = r + noEras - el
				// B(x) <-- inv(discr_r) * lambda(x)
				for i = 0; i <= nroots; i++ {
					if lambda[i] == 0 {
						b[i] = rsA0
					} else {
						b[i] = byte(rsModnn(rs, uint(int(rs.indexOf[lambda[i]])-int(discrR)+nn)))
					}
				}
			} else {
				// B(x) <-- x*B(x)
				copy(b[1:], b[:nroots])
				b[0] = rsA0
			}
			copy(lambda[:nroots+1], t[:nroots+1])
		}
	}

	// Convert lambda to index form and compute deg(lambda(x)).
	degLambda = 0
	for i = 0; i <= nroots; i++ {
		lambda[i] = rs.indexOf[lambda[i]]
		if lambda[i] != rsA0 {
			degLambda = i
		}
	}
	// Find roots of the error+erasure locator polynomial by Chien search.
	copy(reg[1:], lambda[1:1+nroots])
	count := 0
	for i, k = 1, rs.iprim-1; i <= nn; i, k = i+1, int(rsModnn(rs, uint(k+rs.iprim))) {
		q = 1 // lambda[0] is always 1 (index form A0 handling below)
		for j = degLambda; j > 0; j-- {
			if reg[j] != rsA0 {
				reg[j] = byte(rsModnn(rs, uint(reg[j])+uint(j)))
				q ^= uint(rs.alphaTo[reg[j]])
			}
		}
		if q != 0 {
			continue // not a root
		}
		// Store root (index form) and error location number.
		root[count] = uint(i)
		loc[count] = k
		count++
		if count == degLambda {
			break
		}
	}
	if degLambda != count {
		// deg(lambda) unequal to number of roots => uncorrectable
		return -1
	}

	// Compute err+eras evaluator poly omega(x) = s(x)*lambda(x)
	// (modulo x**NROOTS), in index form. Also find deg(omega).
	degOmega = 0
	for i = 0; i < nroots; i++ {
		tmp = 0
		j = degLambda
		if j > i {
			j = i
		}
		for ; j >= 0; j-- {
			if s[i-j] != rsA0 && lambda[j] != rsA0 {
				tmp ^= uint(rs.alphaTo[rsModnn(rs, uint(s[i-j])+uint(lambda[j]))])
			}
		}
		if tmp != 0 {
			degOmega = i
		}
		omega[i] = rs.indexOf[byte(tmp)]
	}
	omega[nroots] = rsA0

	// Compute error values in poly form:
	//   num1 = omega(inv(X(l))), num2 = inv(X(l))**(FCR-1),
	//   den = lambda_pr(inv(X(l))) — all in poly form.
	for j = count - 1; j >= 0; j-- {
		num1 = 0
		for i = degOmega; i >= 0; i-- {
			if omega[i] != rsA0 {
				num1 ^= uint(rs.alphaTo[rsModnn(rs, uint(omega[i])+uint(i)*root[j])])
			}
		}
		num2 = uint(rs.alphaTo[rsModnn(rs, root[j]*uint(rs.fcr-1)+uint(nn))])
		den = 0

		// lambda[i+1] for i even is the formal derivative lambda_pr.
		for i = minInt(uint(degLambda), uint(nroots-1)) &^ 1; i >= 0; i -= 2 {
			if lambda[i+1] != rsA0 {
				den ^= rs.alphaTo[rsModnn(rs, uint(lambda[i+1])+uint(i)*root[j])]
			}
		}
		if den == 0 {
			return -1
		}
		// Apply error to data.
		if num1 != 0 {
			data[loc[j]] ^= rs.alphaTo[rsModnn(rs, uint(rs.indexOf[byte(num1)])+
				uint(rs.indexOf[byte(num2)])+uint(nn)-uint(rs.indexOf[den]))]
		}
	}

	if erasPos != nil {
		for i = 0; i < count; i++ {
			erasPos[i] = loc[i]
		}
	}
	return count
}

func minInt(a, b uint) int {
	if a < b {
		return int(a)
	}
	return int(b)
}

// encodeRs computes the NROOTS parity symbols for a systematic codeword
// (Karn-style). data holds NN-NROOTS message symbols; the parity is
// written to parity. This is provided for testing/verification.
func encodeRs(rs *rsState, data []byte, parity []byte) {
	nroots := rs.nroots
	for i := 0; i < nroots; i++ {
		parity[i] = 0
	}
	for i := 0; i < rs.nn-nroots; i++ {
		feedback := uint(rs.indexOf[data[i]^parity[0]])
		if feedback != rsA0 {
			for j := 1; j < nroots; j++ {
				parity[j] ^= rs.alphaTo[rsModnn(rs, feedback+uint(rs.genpoly[nroots-j]))]
			}
			copy(parity, parity[1:])
			parity[nroots-1] = rs.alphaTo[rsModnn(rs, feedback+uint(rs.genpoly[0]))]
		} else {
			copy(parity, parity[1:])
			parity[nroots-1] = 0
		}
	}
}
