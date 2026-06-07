; Kotlin symbols. class_declaration covers class/interface/enum; reported as
; @class. object_declaration is a singleton/companion; reported as @module.

(class_declaration name: (identifier) @class) @def
(object_declaration name: (identifier) @module) @def
(function_declaration name: (identifier) @function) @def
