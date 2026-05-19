package httpserver

import (
	"fmt"
	"go/ast"
	"go/parser"
	"math"
	"strconv"
	"strings"
)

func evaluateBillingExpression(expr string, vars map[string]float64) (float64, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, fmt.Errorf("billing_expr is required when billing_mode=tiered_expr")
	}
	parsed, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, fmt.Errorf("invalid billing_expr: %w", err)
	}
	value, err := evalBillingExprNode(parsed, vars)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("billing_expr produced an invalid cost")
	}
	return value, nil
}

func evalBillingExprNode(node ast.Expr, vars map[string]float64) (float64, error) {
	switch typed := node.(type) {
	case *ast.BasicLit:
		value, err := strconv.ParseFloat(typed.Value, 64)
		if err != nil {
			return 0, fmt.Errorf("unsupported billing literal %q", typed.Value)
		}
		return value, nil
	case *ast.Ident:
		value, ok := vars[typed.Name]
		if !ok {
			return 0, fmt.Errorf("unsupported billing variable %q", typed.Name)
		}
		return value, nil
	case *ast.ParenExpr:
		return evalBillingExprNode(typed.X, vars)
	case *ast.UnaryExpr:
		value, err := evalBillingExprNode(typed.X, vars)
		if err != nil {
			return 0, err
		}
		switch typed.Op.String() {
		case "+":
			return value, nil
		case "-":
			return -value, nil
		default:
			return 0, fmt.Errorf("unsupported billing unary operator %q", typed.Op.String())
		}
	case *ast.BinaryExpr:
		left, err := evalBillingExprNode(typed.X, vars)
		if err != nil {
			return 0, err
		}
		right, err := evalBillingExprNode(typed.Y, vars)
		if err != nil {
			return 0, err
		}
		switch typed.Op.String() {
		case "+":
			return left + right, nil
		case "-":
			return left - right, nil
		case "*":
			return left * right, nil
		case "/":
			if right == 0 {
				return 0, fmt.Errorf("billing_expr divided by zero")
			}
			return left / right, nil
		default:
			return 0, fmt.Errorf("unsupported billing operator %q", typed.Op.String())
		}
	case *ast.CallExpr:
		name, ok := typed.Fun.(*ast.Ident)
		if !ok {
			return 0, fmt.Errorf("unsupported billing function")
		}
		args := make([]float64, 0, len(typed.Args))
		for _, arg := range typed.Args {
			value, err := evalBillingExprNode(arg, vars)
			if err != nil {
				return 0, err
			}
			args = append(args, value)
		}
		switch name.Name {
		case "min":
			if len(args) != 2 {
				return 0, fmt.Errorf("min requires two arguments")
			}
			return math.Min(args[0], args[1]), nil
		case "max":
			if len(args) != 2 {
				return 0, fmt.Errorf("max requires two arguments")
			}
			return math.Max(args[0], args[1]), nil
		case "ceil":
			if len(args) != 1 {
				return 0, fmt.Errorf("ceil requires one argument")
			}
			return math.Ceil(args[0]), nil
		case "floor":
			if len(args) != 1 {
				return 0, fmt.Errorf("floor requires one argument")
			}
			return math.Floor(args[0]), nil
		default:
			return 0, fmt.Errorf("unsupported billing function %q", name.Name)
		}
	default:
		return 0, fmt.Errorf("unsupported billing expression")
	}
}

func billingExpressionVars(usage usageCounts, metrics meteringMetrics) map[string]float64 {
	inputTokens := usage.InputTokens
	if inputTokens <= 0 {
		inputTokens = metrics.InputTokens
	}
	outputTokens := usage.OutputTokens
	if outputTokens <= 0 {
		outputTokens = metrics.OutputTokens
	}
	requestCount := metrics.RequestCount
	if requestCount <= 0 {
		requestCount = 1
	}
	return map[string]float64{
		"input_tokens":       float64(inputTokens),
		"prompt_tokens":      float64(inputTokens),
		"p":                  float64(inputTokens),
		"output_tokens":      float64(outputTokens),
		"completion_tokens":  float64(outputTokens),
		"c":                  float64(outputTokens),
		"cache_read_tokens":  float64(usage.CacheReadTokens),
		"cache_write_tokens": float64(usage.CacheWriteTokens),
		"image_count":        float64(metrics.ImageCount),
		"images":             float64(metrics.ImageCount),
		"audio_seconds":      metrics.AudioSeconds,
		"request_count":      float64(requestCount),
		"requests":           float64(requestCount),
	}
}
