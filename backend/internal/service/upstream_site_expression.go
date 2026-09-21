package service

import (
	"errors"
	"math"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// Convert nonnegative affine token expressions into component-wise ceilings.
// Every conditional branch is included, so context/time/quality conditions can
// never make a request more expensive than the advertised conservative bound.
// Unsupported nonlinear/task expressions stay unknown and are never routed.
func siteExpressionPrices(expression string, rate float64) (SitePriceTier, error) {
	unknown := errors.New("暂不能确认此动态计费表达式的成本")
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "v1:") {
		expression = strings.TrimSpace(strings.TrimPrefix(expression, "v1:"))
	}
	if len(expression) > 16000 || strings.Contains(expression, "|||") {
		return SitePriceTier{}, unknown
	}
	tree, err := parser.Parse(expression)
	if err != nil {
		return SitePriceTier{}, unknown
	}
	prices, err := siteExpressionBound(tree.Node, 0)
	if err != nil {
		return SitePriceTier{}, unknown
	}
	out := map[string]float64{}
	for k, v := range prices {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return SitePriceTier{}, unknown
		}
		if k == "constant" {
			if v != 0 {
				out["request"] = v / 1e6 * rate
			}
		} else {
			out[k] = v * rate
		}
	}
	unit := "USD/1M tokens"
	if len(out) == 1 {
		if _, ok := out["request"]; ok {
			unit = "USD/request"
		}
	}
	if len(out) == 0 {
		out["request"] = 0
		unit = "USD/request"
	}
	// Token normalization leaves unnamed cache/image subtypes in input/output.
	// Separate ceilings for explicitly named components mirror that convention.
	return SitePriceTier{Key: "default", Unit: unit, Prices: out, Note: "保守价格上限：包含表达式中的全部条件档位；request 项单位为 USD/次"}, nil
}
func siteExpressionBound(node ast.Node, depth int) (map[string]float64, error) {
	fail := errors.New("unsupported expression")
	if depth > 64 {
		return nil, fail
	}
	scalar := func(v float64) (map[string]float64, error) {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fail
		}
		return map[string]float64{"constant": v}, nil
	}
	switch n := node.(type) {
	case *ast.IntegerNode:
		return scalar(float64(n.Value))
	case *ast.FloatNode:
		return scalar(n.Value)
	case *ast.IdentifierNode:
		key := map[string]string{"p": "input_price", "c": "output_price", "cr": "cache_read_price", "cc": "cache_write_price", "cc1h": "cache_write_1h_price", "img": "image_input_price", "img_o": "image_output_price", "ai": "audio_input_price", "ao": "audio_output_price"}[n.Value]
		if key == "" {
			return nil, fail
		}
		return map[string]float64{key: 1}, nil
	case *ast.UnaryNode:
		if n.Operator == "+" {
			return siteExpressionBound(n.Node, depth+1)
		}
	case *ast.ConditionalNode:
		left, err := siteExpressionBound(n.Exp1, depth+1)
		if err != nil {
			return nil, err
		}
		right, err := siteExpressionBound(n.Exp2, depth+1)
		if err != nil {
			return nil, err
		}
		for k, v := range right {
			left[k] = math.Max(left[k], v)
		}
		return left, nil
	case *ast.CallNode:
		if name, ok := n.Callee.(*ast.IdentifierNode); ok && name.Value == "tier" && len(n.Arguments) == 2 {
			return siteExpressionBound(n.Arguments[1], depth+1)
		}
	case *ast.BinaryNode:
		left, err := siteExpressionBound(n.Left, depth+1)
		if err != nil {
			return nil, err
		}
		right, err := siteExpressionBound(n.Right, depth+1)
		if err != nil {
			return nil, err
		}
		switch n.Operator {
		case "+":
			for k, v := range right {
				left[k] += v
			}
			return left, nil
		case "*":
			if factor, ok := right["constant"]; ok && len(right) == 1 {
				for k := range left {
					left[k] *= factor
				}
				return left, nil
			}
			if factor, ok := left["constant"]; ok && len(left) == 1 {
				for k := range right {
					right[k] *= factor
				}
				return right, nil
			}
		case "/":
			// Divisors with conditional bounds are not safe: an upper bound on the
			// divisor is a lower bound on the quotient. Only literal divisors qualify.
			var divisor float64
			switch d := n.Right.(type) {
			case *ast.IntegerNode:
				divisor = float64(d.Value)
			case *ast.FloatNode:
				divisor = d.Value
			}
			if divisor > 0 {
				for k := range left {
					left[k] /= divisor
				}
				return left, nil
			}
		}
	}
	return nil, fail
}
