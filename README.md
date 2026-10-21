# jqe

An interactive terminal editor for [jq](https://jqlang.org) filters.

Point `jqe` at a file containing one or more JSON values (JSON Lines or simply
concatenated), browse the values, expand one, pick fields to **filter on** or to
**project**, and watch the result update live. When you are done, `jqe` prints
the filter so you can reuse it with the real `jq`.

```
 jqe · testdata/sample.jsonl · 4 values · 2 kept · arrays: []
jq> select(.header.sequenceNumber > 0) | .header.session.id
3 7748
4 7750
```

## Install

```sh
go install github.com/rene-d/jqe@latest
```

Requires Go 1.24 or later. The jq engine ([gojq](https://github.com/itchyny/gojq))
is embedded, so `jq` itself is not needed — but if it is installed, `jqe` uses it
to double-check the final filter.

## Usage

```sh
jqe data.json                 # browse and build a filter
cat data.json | jqe           # read from stdin
jqe -e '.header' data.json    # start from an existing filter
jqe -o out.json data.json     # also write the final results (like jq -c)
```

The UI is drawn directly on the terminal (like `fzf`), and on exit **only the
filter** is written to stdout, which makes it easy to compose:

```sh
jq "$(jqe data.json)" data.json     # bash / zsh
jq (jqe data.json) data.json        # fish
jqe data.json > filter.jq           # save it, then: jq -f filter.jq data.json
```

A ready-to-paste command line (`jq '…' 'data.json'`) is also printed to stderr.

### Workflow

1. The list shows each input value on one line — or, once a filter is set, the
   filter's output for that value. Values dropped by the filter are hidden.
2. Press **Enter** to expand a value as a tree (original key order is kept).
3. Move to a field and press:
   - **`f`** to add a condition. It is prefilled with `== <current value>`; edit
     it to anything jq accepts after the path: `!= null`, `> 5`,
     `| test("^02")`, `| startswith("eu")`… This adds `select(<path> <test>)`.
   - **`p`** to toggle a projection. One projection outputs the field itself;
     several build an object `{"name": .path, …}`.

   Fields used in the filter are marked with **●** (condition) or **◆** (projection).
4. Press **`q`** to quit and print the filter.

Conditions are chained (logical AND); projections come last:

```
select(.header.sequenceNumber == 0) | select(.header.session.region == "eu-west") | .header.timeStamp
```

### Arrays

When a path goes through an array, `jqe` by default matches **any element**
rather than the exact index you are looking at:

| Field selected                            | Generated condition                                         |
| ----------------------------------------- | ----------------------------------------------------------- |
| `.payload.records[0].details.parties[1].role` | `select(any(.payload.records[]; any(.details.parties[]; .role == "terminating")))` |

Projections become `[.payload.records[].details.parties[].role]`. Press **`a`**
(or **`ctrl+a`** while typing a condition) to switch to exact indices (`[0]`).

Keys that are not plain identifiers (`record-type`, `iRI-CS-Parameters`, jq
keywords…) are quoted automatically: `."record-type"`.

### Keys

| Key                 | Where       | Action                                          |
| ------------------- | ----------- | ----------------------------------------------- |
| `↑` `↓` / `k` `j`   | everywhere  | move                                            |
| `PgUp` `PgDn` `g` `G` | everywhere | page up / down, top / bottom                   |
| `Enter`             | list        | expand the value                                |
| `←` `→` / `h` `l`   | tree        | collapse / expand (`←` on a leaf goes to parent) |
| `Space`             | tree        | toggle collapse                                 |
| `[` `]`             | tree        | previous / next kept value                      |
| `Esc`               | tree        | back to the list                                |
| `f`                 | tree        | add a condition on the field                    |
| `p`                 | tree        | toggle a projection of the field                |
| `e`                 | everywhere  | edit the whole filter by hand (live preview)    |
| `u`                 | everywhere  | undo the last change                            |
| `X`                 | everywhere  | clear the filter                                |
| `a`                 | everywhere  | arrays: any element `[]` / exact index `[n]`    |
| `q`                 | everywhere  | quit and print the filter                       |
| `ctrl+c`            | everywhere  | quit without printing anything                  |

## Notes and limitations

- The filter is applied to each input value separately, exactly like `jq`
  without `-s`.
- Once the filter has been edited by hand (`e`), `f` and `p` are disabled since
  `jqe` cannot decompose arbitrary jq; `u` brings back the previous state.
- Generated filters only use constructs common to jq and gojq. Hand-edited
  filters are evaluated by gojq, which differs from jq in a few places (see
  [gojq's differences](https://github.com/itchyny/gojq#difference-to-jq)); the
  final check with the real `jq` warns you if it rejects the filter.
- In the preview, objects built by projections have their keys sorted (gojq
  behaviour); `jq` keeps construction order. Unchanged input values keep their
  original key order.
- The whole input is loaded in memory. An evaluation running longer than 10 s
  is aborted, and the preview shows at most 20 outputs per input value.

## Development

```sh
go test ./...     # includes a comparison against the real jq when installed
go build
```

Sample data lives in [`testdata/sample.jsonl`](testdata/sample.jsonl).
