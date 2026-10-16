package tools

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/expr-lang/expr"
)

// calcEnv is everything an expression can reach: numbers, operators and
// these functions. expr has no I/O, so this is the whole surface.
var calcEnv = map[string]any{
	"pi": math.Pi, "e": math.E,
	"sqrt": math.Sqrt, "cbrt": math.Cbrt, "pow": math.Pow, "exp": math.Exp,
	"ln": math.Log, "log10": math.Log10, "log2": math.Log2,
	"sin": math.Sin, "cos": math.Cos, "tan": math.Tan,
	"asin": math.Asin, "acos": math.Acos, "atan": math.Atan, "atan2": math.Atan2,
	"floor": math.Floor, "ceil": math.Ceil, "round": math.Round, "trunc": math.Trunc,
	"hypot": math.Hypot,
	"fabs":  math.Abs,
}

// Calculate evaluates an arithmetic expression such as "2^10 / 3" or
// "sqrt(2) * pi". ^ is exponentiation.
func Calculate(expression string) (string, error) {
	if len(expression) > 500 {
		return "", errors.New("expression is too long")
	}
	src := strings.ReplaceAll(expression, "^", "**")
	prog, err := expr.Compile(src, expr.Env(calcEnv), expr.AsFloat64(), expr.MaxNodes(500))
	if err != nil {
		return "", fmt.Errorf("could not read %q: %w", expression, err)
	}
	out, err := expr.Run(prog, calcEnv)
	if err != nil {
		return "", err
	}
	f := out.(float64)
	if exact, ok := exactInt(expression); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
		// Exact where float64 is not: integers past 2^53.
		return exact, nil
	}
	switch {
	case math.IsNaN(f):
		return "", errors.New("result is not a number")
	case math.IsInf(f, 0):
		return "", errors.New("result is infinite")
	case f == math.Trunc(f) && math.Abs(f) < 1e15:
		return fmt.Sprintf("%.0f", f), nil
	}
	return fmt.Sprintf("%.10g", f), nil
}

// exactInt evaluates expressions of integer literals with + - * / % ^ and
// parentheses in exact rational arithmetic, and returns the result when it
// is a whole number of reasonable length.
func exactInt(src string) (string, bool) {
	p := &ratParser{src: strings.ReplaceAll(src, " ", "")}
	r, ok := p.sum()
	if !ok || p.pos != len(p.src) || !r.IsInt() {
		return "", false
	}
	out := r.Num().String()
	return out, len(out) <= 40
}

type ratParser struct {
	src string
	pos int
}

func (p *ratParser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *ratParser) sum() (*big.Rat, bool) {
	x, ok := p.product()
	for ok && (p.peek() == '+' || p.peek() == '-') {
		op := p.peek()
		p.pos++
		var y *big.Rat
		if y, ok = p.product(); ok {
			if op == '+' {
				x.Add(x, y)
			} else {
				x.Sub(x, y)
			}
		}
	}
	return x, ok
}

func (p *ratParser) product() (*big.Rat, bool) {
	x, ok := p.power()
	for ok && (p.peek() == '*' || p.peek() == '/' || p.peek() == '%') {
		op := p.peek()
		p.pos++
		var y *big.Rat
		if y, ok = p.power(); !ok {
			break
		}
		switch {
		case y.Sign() == 0 && op != '*':
			return nil, false
		case op == '*':
			x.Mul(x, y)
		case op == '/':
			x.Quo(x, y)
		case !x.IsInt() || !y.IsInt():
			return nil, false
		default: // % truncates toward zero, like the float path
			x.SetInt(new(big.Int).Rem(x.Num(), y.Num()))
		}
	}
	return x, ok
}

// power is right-associative and binds tighter than unary minus on its
// left, as in 2^3^2 and -2^2.
func (p *ratParser) power() (*big.Rat, bool) {
	if p.peek() == '-' {
		p.pos++
		x, ok := p.power()
		if ok {
			x.Neg(x)
		}
		return x, ok
	}
	base, ok := p.atom()
	if !ok || p.peek() != '^' {
		return base, ok
	}
	p.pos++
	exp, ok := p.power()
	if !ok || !exp.IsInt() || exp.Sign() < 0 || exp.Num().BitLen() > 8 || !base.IsInt() {
		return nil, false
	}
	return new(big.Rat).SetInt(new(big.Int).Exp(base.Num(), exp.Num(), nil)), true
}

func (p *ratParser) atom() (*big.Rat, bool) {
	if p.peek() == '(' {
		p.pos++
		x, ok := p.sum()
		if !ok || p.peek() != ')' {
			return nil, false
		}
		p.pos++
		return x, true
	}
	start := p.pos
	for p.peek() >= '0' && p.peek() <= '9' {
		p.pos++
	}
	if p.pos == start {
		return nil, false
	}
	return new(big.Rat).SetString(p.src[start:p.pos])
}
