package detect

import "strings"

// A framework claim needs proof. These registries ARE the proof rule: a
// manifest entry is reported as a framework only when it appears here (or
// matches a prefix rule). Everything else in a manifest is a Dependency.
//
// This is deliberately a whitelist rather than a blocklist. A blocklist of
// "typescript, eslint, @types/*" only suppresses the false positives someone
// happened to notice; a whitelist makes every framework in the output
// something recon actually knows about.

// frameworkRules maps dependency names to framework display names, with a
// prefix fallback for ecosystems that namespace their artifacts
// (spring-boot-starter-*, @nestjs/*, ...).
type frameworkRules struct {
	exact  map[string]string
	prefix []prefixRule
}

type prefixRule struct {
	prefix string
	name   string
}

// lookup returns the framework proven by dep, if any.
func (r frameworkRules) lookup(dep string) (string, bool) {
	if r.exact != nil {
		if name, ok := r.exact[dep]; ok {
			return name, true
		}
	}
	for _, p := range r.prefix {
		if strings.HasPrefix(dep, p.prefix) {
			return p.name, true
		}
	}
	return "", false
}

// npmFrameworks recognises application, UI, and test frameworks on npm.
// Toolchain packages (typescript, eslint, prettier, @types/*, bundler plugins)
// are intentionally absent: they are dependencies, not frameworks.
var npmFrameworks = frameworkRules{
	exact: map[string]string{
		"express":               "Express",
		"fastify":               "Fastify",
		"koa":                   "Koa",
		"@hapi/hapi":            "hapi",
		"hapi":                  "hapi",
		"restify":               "Restify",
		"next":                  "Next.js",
		"nuxt":                  "Nuxt",
		"nuxt3":                 "Nuxt",
		"react":                 "React",
		"react-dom":             "React",
		"react-native":          "React Native",
		"expo":                  "Expo",
		"vue":                   "Vue",
		"svelte":                "Svelte",
		"@sveltejs/kit":         "SvelteKit",
		"solid-js":              "Solid",
		"preact":                "Preact",
		"astro":                 "Astro",
		"gatsby":                "Gatsby",
		"ember-source":          "Ember",
		"electron":              "Electron",
		"@remix-run/react":      "Remix",
		"@remix-run/node":       "Remix",
		"jest":                  "Jest",
		"vitest":                "Vitest",
		"mocha":                 "Mocha",
		"jasmine":               "Jasmine",
		"ava":                   "AVA",
		"@playwright/test":      "Playwright",
		"playwright":            "Playwright",
		"cypress":               "Cypress",
		"testcafe":              "TestCafe",
		"prisma":                "Prisma",
		"@prisma/client":        "Prisma",
		"typeorm":               "TypeORM",
		"sequelize":             "Sequelize",
		"mongoose":              "Mongoose",
		"drizzle-orm":           "Drizzle",
		"knex":                  "Knex",
		"graphql":               "GraphQL",
		"apollo-server":         "Apollo Server",
		"@apollo/server":        "Apollo Server",
		"socket.io":             "Socket.IO",
		"redux":                 "Redux",
		"@reduxjs/toolkit":      "Redux Toolkit",
		"zustand":               "Zustand",
		"tailwindcss":           "Tailwind CSS",
		"three":                 "three.js",
		"d3":                    "D3",
		"vite":                  "Vite",
		"webpack":               "webpack",
		"rollup":                "Rollup",
		"esbuild":               "esbuild",
		"@angular/core":         "Angular",
		"@nestjs/core":          "NestJS",
		"@tanstack/react-query": "TanStack Query",
	},
	prefix: []prefixRule{
		{"@nestjs/", "NestJS"},
		{"@angular/", "Angular"},
		{"@sveltejs/", "SvelteKit"},
		{"@remix-run/", "Remix"},
		{"@storybook/", "Storybook"},
	},
}

// pypiFrameworks recognises Python web, task, ML and test frameworks.
var pypiFrameworks = frameworkRules{
	exact: map[string]string{
		"django":              "Django",
		"djangorestframework": "Django REST Framework",
		"flask":               "Flask",
		"fastapi":             "FastAPI",
		"starlette":           "Starlette",
		"tornado":             "Tornado",
		"bottle":              "Bottle",
		"pyramid":             "Pyramid",
		"sanic":               "Sanic",
		"aiohttp":             "aiohttp",
		"litestar":            "Litestar",
		"celery":              "Celery",
		"sqlalchemy":          "SQLAlchemy",
		"alembic":             "Alembic",
		"pydantic":            "Pydantic",
		"scrapy":              "Scrapy",
		"pytest":              "pytest",
		"nose2":               "nose2",
		"hypothesis":          "Hypothesis",
		"torch":               "PyTorch",
		"pytorch-lightning":   "PyTorch Lightning",
		"tensorflow":          "TensorFlow",
		"keras":               "Keras",
		"jax":                 "JAX",
		"transformers":        "Transformers",
		"langchain":           "LangChain",
		"streamlit":           "Streamlit",
		"gradio":              "Gradio",
		"dash":                "Dash",
		"airflow":             "Airflow",
		"apache-airflow":      "Airflow",
		"dbt-core":            "dbt",
	},
}

// goFrameworks is keyed by module path (a trailing /vN is stripped first).
var goFrameworks = frameworkRules{
	exact: map[string]string{
		"github.com/gin-gonic/gin":                  "Gin",
		"github.com/labstack/echo":                  "Echo",
		"github.com/gofiber/fiber":                  "Fiber",
		"github.com/go-chi/chi":                     "chi",
		"github.com/gorilla/mux":                    "Gorilla Mux",
		"github.com/julienschmidt/httprouter":       "httprouter",
		"github.com/beego/beego":                    "Beego",
		"github.com/gobuffalo/buffalo":              "Buffalo",
		"github.com/revel/revel":                    "Revel",
		"github.com/spf13/cobra":                    "Cobra",
		"github.com/urfave/cli":                     "urfave/cli",
		"github.com/spf13/viper":                    "Viper",
		"google.golang.org/grpc":                    "gRPC",
		"gorm.io/gorm":                              "GORM",
		"entgo.io/ent":                              "Ent",
		"github.com/jmoiron/sqlx":                   "sqlx",
		"github.com/stretchr/testify":               "testify",
		"github.com/onsi/ginkgo":                    "Ginkgo",
		"k8s.io/client-go":                          "Kubernetes client-go",
		"github.com/hashicorp/terraform-plugin-sdk": "Terraform Plugin SDK",
		"github.com/charmbracelet/bubbletea":        "Bubble Tea",
	},
}

// jvmFrameworks matches Maven/Gradle artifact ids. JVM ecosystems namespace
// heavily, so prefixes carry most of the weight here.
var jvmFrameworks = frameworkRules{
	exact: map[string]string{
		"hibernate-core": "Hibernate",
		"mockito-core":   "Mockito",
		"guice":          "Guice",
		"dagger":         "Dagger",
		"play":           "Play Framework",
		"grails-core":    "Grails",
	},
	prefix: []prefixRule{
		{"spring-boot", "Spring Boot"},
		{"spring-cloud", "Spring Cloud"},
		{"spring-", "Spring"},
		{"quarkus", "Quarkus"},
		{"micronaut", "Micronaut"},
		{"dropwizard", "Dropwizard"},
		{"vertx-", "Vert.x"},
		{"ktor-", "Ktor"},
		{"jersey-", "Jersey"},
		{"struts", "Struts"},
		{"hibernate-", "Hibernate"},
		{"junit", "JUnit"},
		{"mockito", "Mockito"},
		{"testng", "TestNG"},
		{"assertj", "AssertJ"},
		{"kotlinx-coroutines", "Kotlin Coroutines"},
		{"androidx.", "AndroidX"},
	},
}

// nugetFrameworks matches .NET package ids.
var nugetFrameworks = frameworkRules{
	exact: map[string]string{
		"Dapper":           "Dapper",
		"MediatR":          "MediatR",
		"AutoMapper":       "AutoMapper",
		"Serilog":          "Serilog",
		"Newtonsoft.Json":  "Json.NET",
		"FluentValidation": "FluentValidation",
		"Quartz":           "Quartz.NET",
		"MassTransit":      "MassTransit",
	},
	prefix: []prefixRule{
		{"Microsoft.AspNetCore", "ASP.NET Core"},
		{"Microsoft.EntityFrameworkCore", "Entity Framework Core"},
		{"EntityFramework", "Entity Framework"},
		{"Microsoft.Maui", ".NET MAUI"},
		{"Microsoft.Extensions.Hosting", ".NET Generic Host"},
		{"xunit", "xUnit"},
		{"NUnit", "NUnit"},
		{"MSTest", "MSTest"},
		{"Moq", "Moq"},
		{"Blazor", "Blazor"},
		{"Avalonia", "Avalonia"},
	},
}

// rubyGemFrameworks matches Gemfile gems.
var rubyGemFrameworks = frameworkRules{
	exact: map[string]string{
		"rails":        "Rails",
		"sinatra":      "Sinatra",
		"hanami":       "Hanami",
		"roda":         "Roda",
		"grape":        "Grape",
		"rack":         "Rack",
		"puma":         "Puma",
		"sidekiq":      "Sidekiq",
		"resque":       "Resque",
		"devise":       "Devise",
		"activerecord": "ActiveRecord",
		"sequel":       "Sequel",
		"rspec":        "RSpec",
		"rspec-rails":  "RSpec",
		"minitest":     "Minitest",
		"capybara":     "Capybara",
		"jekyll":       "Jekyll",
	},
}

// cargoFrameworks matches Cargo crate names.
var cargoFrameworks = frameworkRules{
	exact: map[string]string{
		"actix-web": "Actix Web",
		"axum":      "Axum",
		"rocket":    "Rocket",
		"warp":      "warp",
		"tide":      "Tide",
		"poem":      "Poem",
		"tokio":     "Tokio",
		"async-std": "async-std",
		"bevy":      "Bevy",
		"yew":       "Yew",
		"leptos":    "Leptos",
		"dioxus":    "Dioxus",
		"tauri":     "Tauri",
		"diesel":    "Diesel",
		"sea-orm":   "SeaORM",
		"sqlx":      "SQLx",
		"clap":      "clap",
		"tonic":     "tonic",
	},
}

// hexFrameworks matches Elixir hex packages.
var hexFrameworks = frameworkRules{
	exact: map[string]string{
		"phoenix":           "Phoenix",
		"phoenix_live_view": "Phoenix LiveView",
		"ecto":              "Ecto",
		"ecto_sql":          "Ecto",
		"absinthe":          "Absinthe",
		"plug":              "Plug",
		"plug_cowboy":       "Plug",
		"oban":              "Oban",
		"broadway":          "Broadway",
		"nerves":            "Nerves",
		"nx":                "Nx",
	},
}

// pubFrameworks matches Dart/Flutter pub packages.
var pubFrameworks = frameworkRules{
	exact: map[string]string{
		"flutter":          "Flutter",
		"flutter_test":     "Flutter",
		"get":              "GetX",
		"provider":         "Provider",
		"riverpod":         "Riverpod",
		"flutter_riverpod": "Riverpod",
		"bloc":             "Bloc",
		"flutter_bloc":     "Bloc",
		"go_router":        "go_router",
		"dio":              "Dio",
		"shelf":            "Shelf",
		"angel3_framework": "Angel3",
	},
}

// sbtFrameworks matches Scala artifact ids.
var sbtFrameworks = frameworkRules{
	exact: map[string]string{
		"play":         "Play Framework",
		"spark-core":   "Apache Spark",
		"scalatest":    "ScalaTest",
		"specs2-core":  "specs2",
		"munit":        "MUnit",
		"finatra-http": "Finatra",
	},
	prefix: []prefixRule{
		{"akka-http", "Akka HTTP"},
		{"akka", "Akka"},
		{"pekko", "Pekko"},
		{"http4s", "http4s"},
		{"cats-effect", "Cats Effect"},
		{"zio", "ZIO"},
		{"play-", "Play Framework"},
		{"spark-", "Apache Spark"},
	},
}

// normalizeGoModule strips a trailing major-version suffix ("/v2") so
// "github.com/go-chi/chi/v5" matches the registry key.
func normalizeGoModule(path string) string {
	i := strings.LastIndex(path, "/v")
	if i < 0 {
		return path
	}
	rest := path[i+2:]
	if rest == "" {
		return path
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return path
		}
	}
	return path[:i]
}
