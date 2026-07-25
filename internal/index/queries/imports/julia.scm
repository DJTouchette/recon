; Julia include() local includes (package using/import are not local).
;
; @path is the whole argument list, not just a literal string: the
; include(joinpath(@__DIR__, "f.jl")) idiom is common and a literal-only pattern
; drops it. The resolver accepts a bare literal and joinpath forms built from
; literals plus @__DIR__/@__FILE__, and reports anything else unresolvable
; rather than guessing.
(call_expression
  (identifier) @_f
  (argument_list) @path
  (#eq? @_f "include"))
