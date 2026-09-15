# AGENTS.md

## Go Development Guidelines

This repository follows standard Go conventions.

All code changes must prioritize:

1. Readability
2. Simplicity
3. Explicit behavior
4. Testability
5. Maintainability


---

# 1. Formatting

## 1.1 Use gofmt

All Go code must be formatted with:

```bash
gofmt -w .
```

Do not manually align spaces or use custom formatting.


---

## 1.2 Function and method definitions

Keep function signatures compact.

Prefer:

```go
func NewUserService(repo Repository, logger Logger) *UserService {
	return &UserService{
		repo:   repo,
		logger: logger,
	}
}
```

Avoid:

```go
func NewUserService(
	repo Repository,
	logger Logger,
) *UserService {
	return &UserService{
		repo:   repo,
		logger: logger,
	}
}
```


Only split function parameters when the signature becomes genuinely difficult to read.


Example:

```go
func NewVeryComplexService(
	repository Repository,
	permissionManager PermissionManager,
	transactionManager TransactionManager,
	configuration Configuration,
) *Service {
}
```


---

## 1.3 Function calls

Prefer single-line calls when readable.

Good:

```go
user, err := service.CreateUser(ctx, req)
```


Use multiline calls when arguments are long.

Good:

```go
result, err := service.CreateUser(
	ctx,
	CreateUserRequest{
		Name:  userName,
		Email: email,
		Role:  role,
	},
)
```


Avoid unnecessary multiline formatting:

```go
user, err := service.CreateUser(
	ctx,
	req,
)
```


---

# 2. Package Rules


## 2.1 Package naming

Package names must:

- be lowercase
- be short
- avoid underscores
- avoid plural names when possible


Good:

```text
user
config
storage
repository
```

Bad:

```text
user_service
Users
common_utils
```


---

## 2.2 Avoid generic packages


Avoid creating packages named:

```text
utils
common
helpers
base
misc
```

unless the package has a clear and stable responsibility.


Prefer:

```text
encoding
validation
logging
```


---

# 3. Naming Rules


## 3.1 Variables

Use meaningful names.

Good:

```go
userID
requestID
timeout
repository
```


Avoid:

```go
uid
obj
data
tmp
```


unless the scope is extremely small.


---

## 3.2 Acronyms

Use Go naming conventions.

Good:

```go
HTTPClient
JSONParser
URLBuilder
ID
API
```


Bad:

```go
HttpClient
JsonParser
UrlBuilder
Id
```


---

## 3.3 Interfaces

Interface names should describe behavior.

Good:

```go
type Reader interface {
	Read([]byte) error
}
```


Good:

```go
type UserRepository interface {
	FindByID(id string) (*User, error)
}
```


Avoid:

```go
type UserManagerInterface interface {
}
```


Do not add `Interface` suffix.


---

## 3.4 Comments and documentation

When adding or materially changing code:

- Add Go doc comments to exported package API declarations. Start each comment
  with the declared name.
- Add concise comments to unexported code when it carries a security boundary,
  ownership or concurrency rule, lifecycle constraint, important invariant, or
  non-obvious default.
- Explain why the constraint exists or what callers must preserve. Do not merely
  restate the syntax.
- Serialization-only DTOs and obvious one-line helpers do not require comments.
- Update or remove comments whenever the behavior they describe changes.

Good:

```go
// turnState belongs to one turn and prevents evidence from leaking between turns.
type turnState struct {
	mu sync.RWMutex
}
```

Bad:

```go
// turnState is a struct.
type turnState struct {
	mu sync.RWMutex
}
```


---

# 4. Interface Design


## 4.1 Keep interfaces small


Prefer:

```go
type UserReader interface {
	FindByID(id string) (*User, error)
}
```


Avoid:

```go
type UserService interface {
	Create()
	Update()
	Delete()
	Query()
	Export()
}
```


---

## 4.2 Define interfaces near usage

Interfaces should normally be declared by the consumer.

Avoid creating interfaces only for mocking.

Bad:

```go
package service

type Repository interface {
}
```

when only another package needs that abstraction.


---

# 5. Error Handling


## 5.1 Always handle errors


Never ignore returned errors.

Bad:

```go
result, _ := Parse(data)
```


Good:

```go
result, err := Parse(data)
if err != nil {
	return err
}
```


---

## 5.2 Wrap errors with context


Prefer:

```go
return fmt.Errorf(
	"create user failed: %w",
	err,
)
```


Avoid:

```go
return fmt.Errorf(
	"error: %w",
	err,
)
```


Error messages should describe:

- operation
- relevant identifier


---

## 5.3 Do not use panic for normal errors


Avoid:

```go
panic(err)
```


Use:

```go
return err
```


Panic should only be used for unrecoverable programmer errors.


---

# 6. Context Usage


## 6.1 Pass context as first parameter


Good:

```go
func GetUser(
	ctx context.Context,
	id string,
) (*User, error)
```


Do not store context inside structs.


Bad:

```go
type Service struct {
	ctx context.Context
}
```


---

## 6.2 Respect context cancellation


Long-running operations should check context.


Example:

```go
select {
case <-ctx.Done():
	return ctx.Err()

case result := <-ch:
	return result, nil
}
```


---

# 7. Struct Design


## 7.1 Prefer simple structs


Good:

```go
type User struct {
	ID   string
	Name string
}
```


Avoid unnecessary abstraction.


---

## 7.2 Avoid excessive constructors


Do not create constructors without meaningful initialization.


Bad:

```go
func NewUser() *User {
	return &User{}
}
```


Good:

```go
func NewUser(id string, name string) *User {
	return &User{
		ID: id,
		Name: name,
	}
}
```


---

# 8. Dependency Injection


Prefer explicit dependencies.


Good:

```go
type UserService struct {
	repository UserRepository
	logger     Logger
}


func NewUserService(
	repository UserRepository,
	logger Logger,
) *UserService {
	return &UserService{
		repository: repository,
		logger:     logger,
	}
}
```


Avoid:

- global variables
- package-level mutable state
- hidden dependencies


---

# 9. Concurrency


## 9.1 Protect shared state


Any shared mutable state must have synchronization.


Examples:

```go
sync.Mutex

sync.RWMutex

sync.Map
```


---

## 9.2 Goroutine lifecycle must be clear


Every goroutine must have:

- clear owner
- exit condition
- cancellation mechanism


Avoid:

```go
go func() {
	for {
		doWork()
	}
}()
```


without shutdown handling.


---

# 10. Testing Guidelines


## 10.1 Every feature requires tests


New functionality should include:

- unit tests
- integration tests when necessary


Run:

```bash
go test ./...
```


---

## 10.2 Test naming


Use:

```go
func TestUserService_CreateUser(t *testing.T)
```


For cases:

```go
func TestUserService_CreateUser_InvalidEmail(t *testing.T)
```


---

## 10.3 Prefer table-driven tests


Good:

```go
func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "valid email",
			input: "a@test.com",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateEmail(tt.input)

			if got != tt.want {
				t.Fatalf(
					"got %v want %v",
					got,
					tt.want,
				)
			}
		})
	}
}
```


---

## 10.4 Test behavior, not implementation


Prefer:

```go
TestCreateUser_ShouldReturnErrorWhenEmailExists
```


Avoid:

```go
TestCreateUser_CallRepositoryOnce
```


unless testing interaction behavior is the goal.


---

# 11. Mock Rules


## 11.1 Avoid unnecessary mocks


Do not mock every dependency.

Prefer real implementations when:

- fast
- deterministic
- easy to construct


---

## 11.2 Mock external boundaries


Good candidates:

- database
- HTTP API
- message queue
- filesystem


---

# 12. Benchmark Rules


Use benchmarks for:

- performance-sensitive code
- serialization
- algorithms
- memory usage


Example:

```go
func BenchmarkParser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Parse(data)
	}
}
```


Do not add benchmarks without a measurable purpose.


---

# 13. Logging Rules


Logs should provide useful context.


Good:

```go
logger.Info(
	"create user succeeded",
	"user_id",
	user.ID,
)
```


Avoid:

```go
logger.Info("success")
```


Do not log:

- passwords
- tokens
- secrets
- sensitive user data


---

# 14. Static Analysis


Before submitting code, run:

```bash
gofmt -w .
go vet ./...
go test ./...
```


If configured:

```bash
golangci-lint run
```


---

# 15. Git Commit Rules


Use Conventional Commits.


Format:

```text
type(scope): description
```


Examples:

```text
feat(user): add user registration

fix(auth): handle expired token

refactor(storage): simplify repository interface

test(user): add create user tests
```


---

# Final Rule

Write Go code that another Go developer can understand immediately.

Prefer:

- simple code
- explicit dependencies
- small functions
- clear names
- focused tests

Avoid:

- unnecessary abstraction
- clever tricks
- premature optimization
- hidden behavior
