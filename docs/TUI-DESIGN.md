# TUI Design

This document defines the architecture for the Bubble Tea TUI implementation.

## Overall Architecture

The app has a **root model** that manages navigation and routes messages to the **current screen**. Screens are independent models that can be swapped.

```
RootModel
├── currentScreen: Screen (interface)
├── modalStack: []Modal (overlays on top of current screen)
└── db: *DB (async DAO wrapper, passed to screens/modals)
```

**Screen interface:**
```go
type Screen interface {
    Init() tea.Cmd
    Update(tea.Msg) (Screen, tea.Cmd)
    View() string
}
```

**Navigation flow:**
- Root model's `Update()` handles global keys (quit) and screen transitions
- When switching screens, root calls `newScreen.Init()` to kick off data loading
- Modals push onto a stack, receive messages first, pop when done

## Data Flow & Commands

All DB operations go through commands (methods on DAO returning `tea.Cmd`). This keeps `Update()` non-blocking.

**Command methods on DAO:**
```go
func (d *SqliteDao) LoadTodosCmd() tea.Cmd {
    return func() tea.Msg {
        todos, err := d.SelectAllTodos()
        if err != nil {
            return loadErrorMsg{err}
        }
        return todosLoadedMsg{todos}
    }
}

func (d *SqliteDao) CreateTodoCmd(name string, tagID *string, dueDate *string, freq string) tea.Cmd {
    return func() tea.Msg {
        id, err := d.CreateTodo(name, tagID, dueDate, freq)
        if err != nil {
            return mutationErrorMsg{err, "creating todo"}
        }
        return todoCreatedMsg{id}
    }
}
```

**Triggering in Init()** - screen loads its data when activated:
```go
func (m ListScreen) Init() tea.Cmd {
    return tea.Batch(
        m.db.LoadTodosCmd(),
        m.db.LoadTagsCmd(),
    )
}
```

**Triggering in Update()** - user submits a form:
```go
func (m NewTodoForm) Update(msg tea.Msg) (Screen, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        if msg.String() == "enter" && m.focusedOnSubmit() {
            if err := m.validate(); err != nil {
                m.err = err
                return m, nil
            }
            return m, m.db.CreateTodoCmd(m.name, m.tagID, m.dueDate, m.freq)
        }

    case todoCreatedMsg:
        return m, navToListCmd()

    case mutationErrorMsg:
        m.err = msg.err
        return m, nil
    }
}
```

**The flow:**
1. `Init()` returns command(s) to load data
2. Commands run async, send `*LoadedMsg` back
3. `Update()` receives loaded data, stores in model
4. User interacts, `Update()` handles keys, may return mutation command
5. Mutation completes, `Update()` receives result, navigates or shows error

## Async DAO Wrapper

**TUI models must not have access to the blocking DAO.** Instead, they receive a wrapper type that only exposes methods returning `tea.Cmd`:

```go
// DB wraps SqliteDao and only exposes async command methods.
// TUI models hold this, not the underlying SqliteDao.
type DB struct {
    dao *state.SqliteDao  // unexported - models can't call blocking methods
}

func (db *DB) LoadTodosCmd() tea.Cmd { ... }
func (db *DB) LoadTagsCmd() tea.Cmd { ... }
func (db *DB) CreateTodoCmd(...) tea.Cmd { ... }
func (db *DB) UpdateDueByCmd(...) tea.Cmd { ... }
// etc.
```

This enforces the async pattern at the type level - if a screen only has `*DB`, it literally cannot call blocking methods like `SelectAllTodos()` directly. The wrong thing becomes impossible, not just discouraged.

## Message Routing & Bubble Pattern

Messages are routed through the component hierarchy with a bubble-up pattern for unhandled messages.

### Message Categories

Messages are implicitly categorized by how RootModel routes them:

1. **Hard quit** (`ctrl+c`) - Always quits, escape hatch that bypasses all routing
2. **Root-first messages** - Handled by root before any bubbling:
   - `CloseModalMsg` - Pops modal stack (structural change)
   - `ReloadMsg` - Triggers screen reload
   - `WindowSizeMsg` - Updates dimensions, passed to screen
   - `SequenceMsg` - Composite message processed in order (see below)
3. **Navigation messages** - Handled by root to manage component hierarchy:
   - `NavToListMsg`, `OpenModalMsg`, `OpenNewTodoMsg`, etc.
4. **Bubbling messages** - Sent through stack, bubble up when unhandled:
   - `KeyMsg` and other input messages

### SequenceMsg Pattern

`SequenceMsg` allows components to send multiple messages that are processed in guaranteed order. This enables decoupled component communication.

```go
// SequenceMsg is a composite message processed by root in order
type SequenceMsg []tea.Msg
```

**How it works:**
1. Root receives `SequenceMsg`
2. Processes first message in the slice
3. Queues remaining messages for next update cycle
4. Repeat until slice is empty

**Example: Datepicker returning result to parent form**
```go
// Datepicker confirms selection - needs to close AND send result
return m, func() tea.Msg {
    return SequenceMsg{
        CloseModalMsg{},              // First: close datepicker
        DateSelectedMsg{Date: date},  // Then: send to new top (parent form)
    }
}
```

After `CloseModalMsg` pops the datepicker, `DateSelectedMsg` bubbles to the parent form which handles it by populating its date field. The components are decoupled - datepicker doesn't know who opened it, parent just handles `DateSelectedMsg`.

### UnhandledMsg Pattern

Components signal "I didn't handle this" by returning `UnhandledCmd(msg)`:

```go
// In messages.go
type UnhandledMsg struct {
    Original tea.Msg
}

func UnhandledCmd(msg tea.Msg) tea.Cmd {
    return func() tea.Msg { return UnhandledMsg{Original: msg} }
}
```

**Screen/Modal interface contract:**
```go
// Return UnhandledCmd(msg) for messages this component doesn't handle.
Update(msg tea.Msg) (Screen, tea.Cmd)
```

### Bubble Flow

For bubbling messages (e.g., `KeyMsg`):

1. Root sends to topmost modal (if any)
2. If modal returns `UnhandledMsg`, try next modal down
3. If no more modals, try screen
4. If screen returns `UnhandledMsg`, root handles it
5. If root doesn't handle it, message is dropped

**Example: 'q' to quit**
- With no modals: Screen doesn't handle 'q' → returns `UnhandledCmd` → root handles 'q' → quit
- With modal open: Modal handles 'q' (types into textinput) → no bubbling → no quit

### Implementation

RootModel tracks bubble state with `bubbleIndex`:
- `-1` = not bubbling
- `0..len(modalStack)-1` = modal index (0 = top)
- `len(modalStack)` = screen
- `> len(modalStack)` = root handles it

```go
func (m *RootModel) sendToCurrentLayer(msg tea.Msg) (tea.Model, tea.Cmd) {
    if m.bubbleIndex < len(m.modalStack) {
        // Send to modal at index
    } else if m.bubbleIndex == len(m.modalStack) {
        // Send to screen
    } else {
        // Root handles it
    }
}
```

### Component Guidelines

**Screens with embedded components** (e.g., `bubbles/list`):
- Define which keys the embedded component handles
- Return `UnhandledCmd` for keys neither you nor the embedded component handle

```go
func (s *ListScreen) shouldBubbleKey(msg tea.KeyMsg) bool {
    switch msg.String() {
    case "up", "down", "j", "k", "enter", " ", "/", "esc":
        return false  // bubbles/list handles these
    }
    return true  // bubble everything else (like 'q')
}
```

**Modals with textinput**: No special handling needed - textinput handles printable characters by typing them, so 'q' becomes part of the input rather than bubbling.

### Testing

RootModel's message routing behavior is covered by BDD-style tests in `root_test.go`. Tests verify:
- Message bubbling through modal stack and screen
- SequenceMsg processing order
- Modal lifecycle (open, close, result forwarding)
- UnhandledMsg propagation

**When modifying message routing behavior, update the corresponding tests.** The tests serve as executable documentation of the routing contract.

## Navigation & Modal Stack

Navigation happens via messages, not direct screen swaps:

```go
// Navigation messages
type navToListMsg struct{}
type navToNewTodoMsg struct{}
type openModalMsg struct{ modal Modal }
type closeModalMsg struct{ result any }
```

**Root's Update handles navigation:**
```go
func (m RootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    // Modals get first crack at messages
    if len(m.modalStack) > 0 {
        top := m.modalStack[len(m.modalStack)-1]
        newModal, cmd := top.Update(msg)
        m.modalStack[len(m.modalStack)-1] = newModal
        return m, cmd
    }

    switch msg := msg.(type) {
    case navToListMsg:
        m.screen = NewListScreen(m.db)
        return m, m.screen.Init()

    case openModalMsg:
        m.modalStack = append(m.modalStack, msg.modal)
        return m, msg.modal.Init()

    case closeModalMsg:
        m.modalStack = m.modalStack[:len(m.modalStack)-1]
        return m, func() tea.Msg { return modalClosedMsg{msg.result} }
    }

    newScreen, cmd := m.screen.Update(msg)
    m.screen = newScreen
    return m, cmd
}
```

**View composites screen + modals:**
```go
func (m RootModel) View() string {
    v := m.screen.View()
    for _, modal := range m.modalStack {
        v = renderModalOverlay(v, modal.View())
    }
    return v
}
```

## Code Organization

```
cmd/tui/
└── main.go              # entry point only

internal/tui/
├── db.go                # DB wrapper (async command methods)
├── messages.go          # all message types (nav, data loaded, errors)
├── screen.go            # Screen interface
├── modal.go             # Modal interface
├── root.go              # RootModel, navigation handling
├── list.go              # ListScreen
├── todo_form.go         # NewTodoForm modal
├── tag_form.go          # NewTagForm modal
└── date_edit.go         # EditDueDateModal
```

## Sync Safety

The architecture makes it structurally difficult to have stale UI:

1. **Screens load data in `Init()`** - called on every navigation, no way to skip
2. **After mutation, always navigate or reload:**
   ```go
   case todoCreatedMsg:
       return m, navToListCmd()    // new screen calls Init()
       // OR
       return m, m.Init()          // explicit reload
   ```
3. **No direct DAO reads in Update/View** - only through commands. Blocking calls feel wrong.
4. **Screens don't share mutable state** - each has its own copy loaded via `Init()`

## Error Handling

**Load errors** (screen can't show data):
```go
type ListScreen struct {
    todos  []state.Todo
    err    error   // non-nil = show error state
    loaded bool    // false = show loading state
}

func (m ListScreen) View() string {
    if !m.loaded {
        return "Loading..."
    }
    if m.err != nil {
        return fmt.Sprintf("Error: %v\n\n(r)etry or (q)uit", m.err)
    }
    // render list...
}
```

**Mutation errors** (action failed):
```go
case mutationErrorMsg:
    m.err = msg.err    // show inline, user can retry
    m.submitting = false
    return m, nil
```

**Retry pattern:**
```go
case tea.KeyMsg:
    if msg.String() == "r" && m.err != nil {
        m.err = nil
        m.loaded = false
        return m, m.Init()
    }
```

**Validation errors** are sync - checked before mutation command, no async needed.

## UI Theme & Style

### Modal Overlay

Modals render as centered pop-up dialogs floating over the visible background using `bubbletea-overlay` for compositing.

**Styles defined in `modal.go`:**

| Style | Color | Purpose |
|-------|-------|---------|
| `ModalStyle` | `234` (dark grey) | Modal content background |
| `ModalTitleStyle` | `238` (lighter grey) | Modal title, distinguishes from content |
| Border | `62` (muted blue) | Rounded border around modal |

**Design rationale:**
- Dark grey content background creates visual separation from the list behind
- Lighter title background distinguishes the title from form fields
- Text inputs with content blend into the background when filled, visually separating "to fill" from "already filled" fields
- Near-black would lose this blending effect; too-light grey reduces text contrast

### Form Modals

Form modals (New Todo, New Tag, Edit Due Date) follow consistent interaction patterns.

**Layout:**
```
Title

Field 1:   [_______________]
Field 2:   [_______________]
...

   [O̲K]  [C̲ancel]
```

**Navigation:**
- Tab / Down: move to next field or button
- Shift+Tab / Up: move to previous field or button
- Fields and buttons form a single focus cycle

**Key bindings:**
| Key | Action |
|-----|--------|
| Enter on text field | Move to next field |
| Enter on date field | Open datepicker modal |
| Enter on OK button | Submit form |
| Enter on Cancel button | Close modal |
| Alt+O | Submit (from anywhere in form) |
| Alt+C | Cancel (from anywhere in form) |
| Esc | Cancel (unchanged) |

**Visual:**
- Focused button gets highlight style
- Hotkey letters underlined (O in OK, C in Cancel)
