// Purpose: Repository-native tuple-space semantics for Tarsus.
//
// NativeTupleSpace is the local serialization primitive used by the distributed
// tuple owner. It is a multiset: multiple tuples may have the same name and
// value. Put publishes one tuple, Read returns a matching tuple without
// consuming it, and Get atomically removes and returns one matching tuple.
package tuplespace

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

var (
	// ErrTupleNotFound is returned when no tuple currently matches an expression.
	ErrTupleNotFound = errors.New("no matching tuple")
	// ErrInvalidTuplePattern is returned when a regular expression cannot be compiled.
	ErrInvalidTuplePattern = errors.New("invalid tuple pattern")
)

// NativeTuple is one entry in the tuple-space multiset.
type NativeTuple struct {
	Name  string
	Value []byte
}

// NativeTupleSpace implements the tuple-space operations inside this
// repository. The mutex is also the local linearization boundary: matching and
// removal happen in one critical section, so at most one concurrent Get can
// consume a particular tuple.
//
// Expressions use these forms:
//   - no pattern metacharacters: exact tuple-name match
//   - '*' only: shell-style wildcard match (for example, "task:image:*")
//   - regular-expression metacharacters: Go regular expression match
//
// Regex expressions are anchored to the entire tuple name. This prevents a
// pattern such as "task:.+" from unexpectedly matching an unrelated prefix.
type NativeTupleSpace struct {
	mu                sync.Mutex
	tuples            []NativeTuple
	permissionChecker PermissionChecker
}

// NewNativeTupleSpace creates an empty repository-native tuple space.
func NewNativeTupleSpace() *NativeTupleSpace {
	return &NativeTupleSpace{}
}

// SetPermissionChecker installs an optional operation-level permission check.
func (n *NativeTupleSpace) SetPermissionChecker(checker PermissionChecker) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.permissionChecker = checker
}

// TsPut adds one tuple to the multiset. Input bytes are copied so callers may
// safely reuse their buffer after the call returns.
func (n *NativeTupleSpace) TsPut(tpname string, tpvalue []byte) (int, error) {
	if tpname == "" {
		return TSPUT_ER, errors.New("tuple name required")
	}
	if len(tpvalue) == 0 {
		return TSPUT_ER, errors.New("tuple value required")
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.permissionChecker != nil {
		if err := n.permissionChecker.CheckPermission(OpTsPut); err != nil {
			return TSPUT_ER, err
		}
	}
	n.tuples = append(n.tuples, NativeTuple{
		Name:  tpname,
		Value: append([]byte(nil), tpvalue...),
	})
	return 0, nil
}

// TsReplace atomically removes every tuple with the exact name and publishes
// one replacement value. This optional application-level operation preserves
// TsPut's Linda-style multiset behavior while supporting singleton records
// such as renewable storage advertisements.
func (n *NativeTupleSpace) TsReplace(tpname string, tpvalue []byte) (int, error) {
	if tpname == "" {
		return TSPUT_ER, errors.New("tuple name required")
	}
	if isTuplePattern(tpname) {
		return TSPUT_ER, errors.New("tuple replacement requires an exact name")
	}
	if len(tpvalue) == 0 {
		return TSPUT_ER, errors.New("tuple value required")
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.permissionChecker != nil {
		if err := n.permissionChecker.CheckPermission(OpTsPut); err != nil {
			return TSPUT_ER, err
		}
	}
	kept := n.tuples[:0]
	for _, tuple := range n.tuples {
		if tuple.Name != tpname {
			kept = append(kept, tuple)
		}
	}
	n.tuples = append(kept, NativeTuple{
		Name:  tpname,
		Value: append([]byte(nil), tpvalue...),
	})
	return 0, nil
}

// TsRead returns the oldest matching tuple without consuming it.
func (n *NativeTupleSpace) TsRead(expr string) ([]byte, error) {
	match, err := compileTupleMatcher(expr)
	if err != nil {
		return nil, err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.permissionChecker != nil {
		if err := n.permissionChecker.CheckPermission(OpTsRead); err != nil {
			return nil, err
		}
	}
	for _, tuple := range n.tuples {
		if match(tuple.Name) {
			return append([]byte(nil), tuple.Value...), nil
		}
	}
	return nil, ErrTupleNotFound
}

// TsGet atomically removes and returns the oldest matching tuple.
func (n *NativeTupleSpace) TsGet(expr string) ([]byte, error) {
	match, err := compileTupleMatcher(expr)
	if err != nil {
		return nil, err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.permissionChecker != nil {
		if err := n.permissionChecker.CheckPermission(OpTsGet); err != nil {
			return nil, err
		}
	}
	for i, tuple := range n.tuples {
		if !match(tuple.Name) {
			continue
		}
		value := append([]byte(nil), tuple.Value...)
		copy(n.tuples[i:], n.tuples[i+1:])
		n.tuples[len(n.tuples)-1] = NativeTuple{}
		n.tuples = n.tuples[:len(n.tuples)-1]
		return value, nil
	}
	return nil, ErrTupleNotFound
}

// Len returns the number of currently stored tuples. It is intended for
// diagnostics and tests rather than distributed coordination decisions.
func (n *NativeTupleSpace) Len() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.tuples)
}

func compileTupleMatcher(expr string) (func(string) bool, error) {
	if expr == "" {
		return nil, fmt.Errorf("%w: empty expression", ErrInvalidTuplePattern)
	}

	const regexMeta = `.+?^$[]{}|()\`
	if !strings.Contains(expr, "*") && !strings.ContainsAny(expr, regexMeta) {
		return func(name string) bool { return name == expr }, nil
	}

	pattern := expr
	if !strings.ContainsAny(expr, regexMeta) {
		// Treat '*' as the only wildcard and quote every other character.
		parts := strings.Split(expr, "*")
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		pattern = strings.Join(parts, ".*")
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTuplePattern, err)
	}
	return re.MatchString, nil
}
