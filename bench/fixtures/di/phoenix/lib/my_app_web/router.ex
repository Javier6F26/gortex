defmodule MyAppWeb.Router do
  use Phoenix.Router

  scope "/", MyAppWeb do
    resources "/users", UserController
  end
end
