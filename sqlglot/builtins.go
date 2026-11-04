package sqlglot

import "strings"

type BuiltinDependency string

const (
	DependencyEnvironment BuiltinDependency = "environment"
	DependencySecurity    BuiltinDependency = "security"
	DependencySession     BuiltinDependency = "session"
	DependencyTime        BuiltinDependency = "time"
	DependencyRandomness  BuiltinDependency = "randomness"
)

// BuiltinFunction describes a built-in that depends on state outside its arguments.
type BuiltinFunction struct {
	Name       string
	Dependency BuiltinDependency
}

var tsqlBuiltinDependencies = map[string]BuiltinDependency{
	"DB_NAME": DependencyEnvironment, "DB_ID": DependencyEnvironment,
	"SERVERPROPERTY": DependencyEnvironment, "DATABASEPROPERTYEX": DependencyEnvironment, "@@SERVERNAME": DependencyEnvironment,
	"CURRENT_USER": DependencySecurity, "SESSION_USER": DependencySecurity, "SYSTEM_USER": DependencySecurity,
	"USER_NAME": DependencySecurity, "SUSER_SNAME": DependencySecurity, "ORIGINAL_LOGIN": DependencySecurity, "IS_MEMBER": DependencySecurity,
	"APP_NAME": DependencySession, "HOST_NAME": DependencySession, "CONNECTIONPROPERTY": DependencySession,
	"CONTEXT_INFO": DependencySession, "SESSION_CONTEXT": DependencySession,
	"GETDATE": DependencyTime, "GETUTCDATE": DependencyTime, "SYSDATETIME": DependencyTime,
	"SYSUTCDATETIME": DependencyTime, "SYSDATETIMEOFFSET": DependencyTime, "CURRENT_TIMESTAMP": DependencyTime,
	"NEWID": DependencyRandomness, "NEWSEQUENTIALID": DependencyRandomness, "RAND": DependencyRandomness,
}

var tsqlBuiltinClasses = map[string]string{
	"CurrentTimestamp": "CURRENT_TIMESTAMP", "CurrentTimestampLTZ": "SYSDATETIMEOFFSET",
	"CurrentUser": "CURRENT_USER", "SessionUser": "SESSION_USER", "Uuid": "NEWID", "Rand": "RAND",
}

// LookupBuiltin looks up the mapped state-dependent built-ins for a dialect.
// A false result means unmapped, not pure.
func LookupBuiltin(name, dialect string) (BuiltinFunction, bool) {
	switch strings.ToLower(dialect) {
	case "tsql", "sqlserver", "mssql":
		name = strings.ToUpper(name)
		if dependency, ok := tsqlBuiltinDependencies[name]; ok {
			return BuiltinFunction{Name: name, Dependency: dependency}, true
		}
	}
	return BuiltinFunction{}, false
}

// Builtin identifies a mapped built-in call or system variable in an expression.
// It excludes ordinary identifiers and schema-qualified user functions.
func (e *Expression) Builtin(dialect string) (BuiltinFunction, bool) {
	if e == nil || (e.Parent != nil && e.Parent.Class == "Dot" && e.Parent.Args["expression"] == e) {
		return BuiltinFunction{}, false
	}
	if e.Class == "Anonymous" {
		return LookupBuiltin(e.Name(), dialect)
	}
	if e.Class == "Parameter" && e.This() != nil && e.This().Class == "Parameter" && e.This().This() != nil && e.This().This().Class == "Var" {
		return LookupBuiltin("@@"+e.This().This().Name(), dialect)
	}
	if name, ok := tsqlBuiltinClasses[e.Class]; ok {
		if original, ok := e.Meta["builtinName"].(string); ok {
			name = original
		}
		return LookupBuiltin(name, dialect)
	}
	return BuiltinFunction{}, false
}

func (p *parser) rememberBuiltin(e *Expression, name string) *Expression {
	if builtin, ok := LookupBuiltin(name, p.dialect); ok && e != nil {
		if e.Meta == nil {
			e.Meta = map[string]any{}
		}
		e.Meta["builtinName"] = builtin.Name
	}
	return e
}
