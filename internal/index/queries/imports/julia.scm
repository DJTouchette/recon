; Julia include("file.jl") local includes (package using/import are not local).
(call_expression
  (identifier) @_f
  (argument_list (string_literal (content) @path))
  (#eq? @_f "include"))
