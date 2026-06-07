; Lua symbols. Lua has no classes; functions and methods are the symbols.

(function_declaration
  name: [
    (identifier) @function
    (dot_index_expression field: (identifier) @function)
  ]) @def
(function_declaration
  name: (method_index_expression method: (identifier) @method)) @def

; name = function(...) and tbl.name = function(...)
(assignment_statement
  (variable_list . name: [
    (identifier) @function
    (dot_index_expression field: (identifier) @function)
  ])
  (expression_list . value: (function_definition))) @def
