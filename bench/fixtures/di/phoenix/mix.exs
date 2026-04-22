defmodule MyApp.MixProject do
  # Not built — static corpus for the Gortex indexer.
  use Mix.Project

  def project do
    [
      app: :my_app,
      version: "0.0.0",
      elixir: "~> 1.14",
      deps: [{:phoenix, "~> 1.7"}]
    ]
  end
end
