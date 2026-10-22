package ui

import (
	"errors"
	"math/big"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"tui-ssh-workbench/internal/infra/sftpclient"
)

type sftpEntryFilter struct {
	raw           string
	expression    *sftpFilterExpression
	includeNames  []string
	excludeNames  []string
	includeTypes  map[string]struct{}
	excludeTypes  map[string]struct{}
	includeFields []sftpEntryPredicate
	excludeFields []sftpEntryPredicate
}

type sftpFilterExpression struct {
	operator string
	atom     sftpFilterExpressionAtom
	left     *sftpFilterExpression
	right    *sftpFilterExpression
}

type sftpFilterExpressionAtom struct {
	kind      string
	value     string
	predicate sftpEntryPredicate
}

type sftpEntryPredicate struct {
	field    string
	operator string
	size     int64
	mtime    time.Time
	mode     os.FileMode
	id       uint32
}

func parseSFTPEntryFilter(value string) (sftpEntryFilter, error) {
	filter := sftpEntryFilter{raw: strings.TrimSpace(value)}
	if filter.raw == "" {
		return filter, nil
	}
	if strings.HasPrefix(strings.ToLower(filter.raw), "expr:") {
		expression, err := parseSFTPFilterExpression(strings.TrimSpace(filter.raw[len("expr:"):]))
		if err != nil {
			return sftpEntryFilter{}, err
		}
		filter.expression = expression
		return filter, nil
	}
	for _, rawTerm := range strings.Split(filter.raw, ",") {
		term := strings.TrimSpace(rawTerm)
		if term == "" {
			continue
		}
		exclude := strings.HasPrefix(term, "!")
		if exclude {
			term = strings.TrimSpace(strings.TrimPrefix(term, "!"))
			if term == "" {
				return sftpEntryFilter{}, errors.New("过滤规则中的 ! 后需要填写模式或类型")
			}
		}
		lower := strings.ToLower(term)
		if predicate, ok, err := parseSFTPEntryPredicate(term); ok {
			if err != nil {
				return sftpEntryFilter{}, err
			}
			if exclude {
				filter.excludeFields = append(filter.excludeFields, predicate)
			} else {
				filter.includeFields = append(filter.includeFields, predicate)
			}
			continue
		}
		if strings.HasPrefix(lower, "type:") {
			kind := strings.TrimPrefix(lower, "type:")
			switch kind {
			case "file", "dir", "link", "special":
			default:
				return sftpEntryFilter{}, errors.New("类型过滤仅支持 file、dir、link、special")
			}
			if exclude {
				if filter.excludeTypes == nil {
					filter.excludeTypes = make(map[string]struct{})
				}
				filter.excludeTypes[kind] = struct{}{}
			} else {
				if filter.includeTypes == nil {
					filter.includeTypes = make(map[string]struct{})
				}
				filter.includeTypes[kind] = struct{}{}
			}
			continue
		}
		pattern := strings.ToLower(term)
		if err := validateSFTPFilterNamePattern(pattern); err != nil {
			return sftpEntryFilter{}, err
		}
		if exclude {
			filter.excludeNames = append(filter.excludeNames, pattern)
		} else {
			filter.includeNames = append(filter.includeNames, pattern)
		}
	}
	return filter, nil
}

func parseSFTPFilterExpression(value string) (*sftpFilterExpression, error) {
	tokens, err := tokenizeSFTPFilterExpression(value)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errors.New("expr: 后需要填写过滤表达式")
	}
	parser := sftpFilterExpressionParser{tokens: tokens}
	expression, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	if parser.index != len(parser.tokens) {
		return nil, errors.New("过滤表达式中有多余内容或缺少逻辑运算符")
	}
	return expression, nil
}

func tokenizeSFTPFilterExpression(value string) ([]string, error) {
	tokens := make([]string, 0)
	for index := 0; index < len(value); {
		current := value[index]
		if current == ' ' || current == '\t' || current == '\r' || current == '\n' {
			index++
			continue
		}
		if current == '(' || current == ')' || current == '!' {
			tokens = append(tokens, string(current))
			index++
			continue
		}
		if strings.HasPrefix(value[index:], "&&") || strings.HasPrefix(value[index:], "||") {
			tokens = append(tokens, value[index:index+2])
			index += 2
			continue
		}
		if current == '&' || current == '|' {
			return nil, errors.New("逻辑运算符请使用 && 或 ||")
		}
		start := index
		for index < len(value) {
			current = value[index]
			if current == ' ' || current == '\t' || current == '\r' || current == '\n' || current == '(' || current == ')' || current == '&' || current == '|' {
				break
			}
			index++
		}
		if start == index {
			return nil, errors.New("过滤表达式无法解析")
		}
		tokens = append(tokens, value[start:index])
	}
	return tokens, nil
}

type sftpFilterExpressionParser struct {
	tokens []string
	index  int
}

func (parser *sftpFilterExpressionParser) parseOr() (*sftpFilterExpression, error) {
	left, err := parser.parseAnd()
	if err != nil {
		return nil, err
	}
	for parser.consume("||") {
		right, err := parser.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &sftpFilterExpression{operator: "or", left: left, right: right}
	}
	return left, nil
}

func (parser *sftpFilterExpressionParser) parseAnd() (*sftpFilterExpression, error) {
	left, err := parser.parseUnary()
	if err != nil {
		return nil, err
	}
	for parser.consume("&&") {
		right, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &sftpFilterExpression{operator: "and", left: left, right: right}
	}
	return left, nil
}

func (parser *sftpFilterExpressionParser) parseUnary() (*sftpFilterExpression, error) {
	if parser.consume("!") {
		child, err := parser.parseUnary()
		if err != nil {
			return nil, err
		}
		return &sftpFilterExpression{operator: "not", left: child}, nil
	}
	if parser.consume("(") {
		expression, err := parser.parseOr()
		if err != nil {
			return nil, err
		}
		if !parser.consume(")") {
			return nil, errors.New("过滤表达式缺少右括号 )")
		}
		return expression, nil
	}
	if parser.index >= len(parser.tokens) || parser.tokens[parser.index] == ")" || parser.tokens[parser.index] == "&&" || parser.tokens[parser.index] == "||" {
		return nil, errors.New("过滤表达式缺少条件")
	}
	token := parser.tokens[parser.index]
	parser.index++
	atom, err := parseSFTPFilterExpressionAtom(token)
	if err != nil {
		return nil, err
	}
	return &sftpFilterExpression{operator: "atom", atom: atom}, nil
}

func (parser *sftpFilterExpressionParser) consume(token string) bool {
	if parser.index >= len(parser.tokens) || parser.tokens[parser.index] != token {
		return false
	}
	parser.index++
	return true
}

func parseSFTPFilterExpressionAtom(token string) (sftpFilterExpressionAtom, error) {
	lower := strings.ToLower(token)
	if strings.HasPrefix(lower, "name:") {
		pattern := strings.TrimSpace(token[len("name:"):])
		if err := validateSFTPFilterNamePattern(pattern); err != nil {
			return sftpFilterExpressionAtom{}, err
		}
		return sftpFilterExpressionAtom{kind: "name", value: strings.ToLower(pattern)}, nil
	}
	if strings.HasPrefix(lower, "type:") {
		kind := strings.TrimPrefix(lower, "type:")
		switch kind {
		case "file", "dir", "link", "special":
			return sftpFilterExpressionAtom{kind: "type", value: kind}, nil
		default:
			return sftpFilterExpressionAtom{}, errors.New("类型过滤仅支持 file、dir、link、special")
		}
	}
	if predicate, ok, err := parseSFTPEntryPredicate(token); ok {
		if err != nil {
			return sftpFilterExpressionAtom{}, err
		}
		return sftpFilterExpressionAtom{kind: "predicate", predicate: predicate}, nil
	}
	if err := validateSFTPFilterNamePattern(token); err != nil {
		return sftpFilterExpressionAtom{}, err
	}
	return sftpFilterExpressionAtom{kind: "name", value: strings.ToLower(token)}, nil
}

func validateSFTPFilterNamePattern(pattern string) error {
	if pattern == "" {
		return errors.New("文件名过滤缺少 glob 模式")
	}
	if strings.HasPrefix(pattern, "/") {
		return errors.New("路径 glob 必须是相对于当前过滤根目录的路径")
	}
	for _, component := range strings.Split(pattern, "/") {
		if component == ".." {
			return errors.New("路径 glob 不能包含 .. 路径段")
		}
	}
	if _, err := path.Match(strings.ToLower(pattern), ""); err != nil {
		return errors.New("无效的文件名或相对路径模式：" + pattern)
	}
	return nil
}

func (expression *sftpFilterExpression) matches(entry sftpclient.Entry, relativePath string) bool {
	if expression == nil {
		return false
	}
	switch expression.operator {
	case "and":
		return expression.left.matches(entry, relativePath) && expression.right.matches(entry, relativePath)
	case "or":
		return expression.left.matches(entry, relativePath) || expression.right.matches(entry, relativePath)
	case "not":
		return !expression.left.matches(entry, relativePath)
	case "atom":
		switch expression.atom.kind {
		case "name":
			return matchesSFTPFilterName(expression.atom.value, entry.Name, relativePath)
		case "type":
			return sftpEntryKind(entry) == expression.atom.value
		case "predicate":
			return matchesSFTPEntryPredicate(entry, expression.atom.predicate)
		}
	}
	return false
}

func parseSFTPEntryPredicate(term string) (sftpEntryPredicate, bool, error) {
	lower := strings.ToLower(term)
	field := ""
	for _, candidate := range []string{"size", "mtime", "perm", "uid", "gid"} {
		if strings.HasPrefix(lower, candidate) {
			rest := strings.TrimSpace(lower[len(candidate):])
			if strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, "<") || strings.HasPrefix(rest, "=") {
				field = candidate
				break
			}
		}
	}
	if field == "" {
		return sftpEntryPredicate{}, false, nil
	}

	rest := strings.TrimSpace(term[len(field):])
	operator := ""
	for _, candidate := range []string{">=", "<=", "=", ">", "<"} {
		if strings.HasPrefix(rest, candidate) {
			operator = candidate
			rest = strings.TrimSpace(strings.TrimPrefix(rest, candidate))
			break
		}
	}
	if rest == "" {
		return sftpEntryPredicate{}, true, errors.New("高级过滤规则缺少比较值")
	}
	predicate := sftpEntryPredicate{field: field, operator: operator}
	switch field {
	case "size":
		size, err := parseSFTPFilterSize(rest)
		if err != nil {
			return sftpEntryPredicate{}, true, err
		}
		predicate.size = size
	case "mtime":
		parsed, err := time.Parse(time.RFC3339, rest)
		if err != nil {
			return sftpEntryPredicate{}, true, errors.New("修改时间请使用 RFC3339 格式，例如 2025-01-31T12:00:00Z")
		}
		predicate.mtime = parsed
	case "perm":
		if operator != "=" {
			return sftpEntryPredicate{}, true, errors.New("权限过滤仅支持 perm=八进制权限，例如 perm=0644")
		}
		if len(rest) < 3 || len(rest) > 4 {
			return sftpEntryPredicate{}, true, errors.New("权限过滤请使用三位或四位八进制数，例如 perm=0644")
		}
		for _, digit := range rest {
			if digit < '0' || digit > '7' {
				return sftpEntryPredicate{}, true, errors.New("权限过滤请使用八进制数，例如 perm=0644")
			}
		}
		mode, err := strconv.ParseUint(rest, 8, 32)
		if err != nil {
			return sftpEntryPredicate{}, true, errors.New("权限过滤数值无效")
		}
		predicate.mode = os.FileMode(mode) & 0o777
	case "uid", "gid":
		if strings.HasPrefix(rest, "+") || strings.HasPrefix(rest, "-") {
			return sftpEntryPredicate{}, true, errors.New("属主过滤请使用 0 到 4294967295 之间的数字")
		}
		id, err := strconv.ParseUint(rest, 10, 32)
		if err != nil {
			return sftpEntryPredicate{}, true, errors.New("属主过滤请使用 0 到 4294967295 之间的数字")
		}
		predicate.id = uint32(id)
	}
	return predicate, true, nil
}

func parseSFTPFilterSize(value string) (int64, error) {
	units := []struct {
		suffix     string
		multiplier int64
	}{
		{"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10}, {"b", 1},
	}
	multiplier := int64(1)
	number := strings.TrimSpace(value)
	for _, unit := range units {
		if strings.HasSuffix(strings.ToLower(number), unit.suffix) {
			number = strings.TrimSpace(number[:len(number)-len(unit.suffix)])
			multiplier = unit.multiplier
			break
		}
	}
	if !validSFTPFilterDecimal(number) {
		return 0, errors.New("文件大小请使用非负数，可选单位 B、KiB、MiB 或 GiB")
	}
	amount, ok := new(big.Rat).SetString(number)
	if !ok {
		return 0, errors.New("文件大小数值无效")
	}
	amount.Mul(amount, new(big.Rat).SetInt64(multiplier))
	if !amount.IsInt() || !amount.Num().IsInt64() {
		return 0, errors.New("文件大小超出范围或不能表示为整数字节")
	}
	return amount.Num().Int64(), nil
}

func validSFTPFilterDecimal(value string) bool {
	if value == "" {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return false
	}
	for _, part := range parts {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}

func filterSFTPEntries(entries []sftpclient.Entry, filter sftpEntryFilter) []sftpclient.Entry {
	if filter.raw == "" {
		return entries
	}
	filtered := make([]sftpclient.Entry, 0, len(entries))
	for _, entry := range entries {
		if matchesSFTPEntryFilter(entry, filter) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func matchesSFTPEntryFilter(entry sftpclient.Entry, filter sftpEntryFilter) bool {
	return matchesSFTPEntryFilterAtPath(entry, filter, entry.Name)
}

func matchesSFTPEntryFilterAtPath(entry sftpclient.Entry, filter sftpEntryFilter, relativePath string) bool {
	if filter.raw == "" {
		return true
	}
	if filter.expression != nil {
		return filter.expression.matches(entry, relativePath)
	}
	kind := sftpEntryKind(entry)
	if _, excluded := filter.excludeTypes[kind]; excluded {
		return false
	}
	if len(filter.includeTypes) > 0 {
		if _, included := filter.includeTypes[kind]; !included {
			return false
		}
	}
	name := strings.ToLower(entry.Name)
	for _, pattern := range filter.excludeNames {
		if matchesSFTPFilterName(pattern, name, relativePath) {
			return false
		}
	}
	if matchesAnySFTPEntryPredicate(entry, filter.excludeFields) {
		return false
	}
	if len(filter.includeNames) > 0 {
		matchedAny := false
		for _, pattern := range filter.includeNames {
			if matchesSFTPFilterName(pattern, name, relativePath) {
				matchedAny = true
				break
			}
		}
		if !matchedAny {
			return false
		}
	}
	return matchesAllSFTPEntryPredicates(entry, filter.includeFields)
}

func matchesSFTPFilterName(pattern, entryName, relativePath string) bool {
	name := entryName
	if strings.Contains(pattern, "/") {
		name = relativePath
	}
	matched, _ := path.Match(pattern, strings.ToLower(name))
	return matched
}

func matchesAnySFTPEntryPredicate(entry sftpclient.Entry, predicates []sftpEntryPredicate) bool {
	for _, predicate := range predicates {
		if matchesSFTPEntryPredicate(entry, predicate) {
			return true
		}
	}
	return false
}

func matchesAllSFTPEntryPredicates(entry sftpclient.Entry, predicates []sftpEntryPredicate) bool {
	for _, predicate := range predicates {
		if !matchesSFTPEntryPredicate(entry, predicate) {
			return false
		}
	}
	return true
}

func matchesSFTPEntryPredicate(entry sftpclient.Entry, predicate sftpEntryPredicate) bool {
	var comparison int
	switch predicate.field {
	case "size":
		comparison = compareSFTPFilterInt(entry.Size, predicate.size)
	case "mtime":
		if entry.ModTime.IsZero() {
			return false
		}
		switch {
		case entry.ModTime.Before(predicate.mtime):
			comparison = -1
		case entry.ModTime.After(predicate.mtime):
			comparison = 1
		}
	case "perm":
		if !entry.ModeKnown {
			return false
		}
		comparison = compareSFTPFilterInt(int64(entry.Mode.Perm()), int64(predicate.mode.Perm()))
	case "uid":
		if !entry.OwnershipKnown {
			return false
		}
		comparison = compareSFTPFilterInt(int64(entry.UID), int64(predicate.id))
	case "gid":
		if !entry.OwnershipKnown {
			return false
		}
		comparison = compareSFTPFilterInt(int64(entry.GID), int64(predicate.id))
	default:
		return false
	}
	switch predicate.operator {
	case "=":
		return comparison == 0
	case ">":
		return comparison > 0
	case ">=":
		return comparison >= 0
	case "<":
		return comparison < 0
	case "<=":
		return comparison <= 0
	default:
		return false
	}
}

func compareSFTPFilterInt(left, right int64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func sftpEntryKind(entry sftpclient.Entry) string {
	switch {
	case entry.IsSymlink:
		return "link"
	case entry.IsDir:
		return "dir"
	case entry.Mode.IsRegular():
		return "file"
	default:
		return "special"
	}
}

func sftpFilterForSide(state *sftpBrowserState, side sftpSide) sftpEntryFilter {
	if state == nil {
		return sftpEntryFilter{}
	}
	if side == sftpRemoteSide {
		return state.remoteFilter
	}
	return state.localFilter
}

func sftpFilteredPaneEntries(state *sftpBrowserState, side sftpSide) []sftpclient.Entry {
	if state == nil {
		return nil
	}
	var entries, cached []sftpclient.Entry
	if side == sftpRemoteSide {
		entries, cached = state.entries, state.remoteFilteredEntries
	} else {
		entries, cached = state.localEntries, state.localFilteredEntries
	}
	filter := sftpFilterForSide(state, side)
	if filter.raw == "" {
		return entries
	}
	if cached != nil {
		return cached
	}
	return filterSFTPEntries(entries, filter)
}

func rebuildSFTPEntryViews(state *sftpBrowserState) {
	if state == nil {
		return
	}
	state.remoteFilteredEntries = filterSFTPEntries(state.entries, state.remoteFilter)
	state.localFilteredEntries = filterSFTPEntries(state.localEntries, state.localFilter)
}

func setSFTPFilter(state *sftpBrowserState, side sftpSide, filter sftpEntryFilter) {
	if state == nil {
		return
	}
	if side == sftpRemoteSide {
		state.remoteFilter = filter
		state.selected, state.top = 0, 0
	} else {
		state.localFilter = filter
		state.localSelected, state.localTop = 0, 0
	}
	rebuildSFTPEntryViews(state)
}
