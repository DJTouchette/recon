; Julia symbols. Names are positional children inside signatures/type_heads.

(function_definition (signature (call_expression . (identifier) @function))) @def
(function_definition (signature (where_expression (call_expression . (identifier) @function)))) @def
(function_definition (signature (typed_expression (call_expression . (identifier) @function)))) @def
(macro_definition (signature (call_expression . (identifier) @macro))) @def
(struct_definition (type_head (identifier) @struct)) @def
(abstract_definition (type_head (identifier) @type)) @def
(module_definition name: (identifier) @module) @def
