defmodule MyAppWeb.UserController do
  use Phoenix.Controller, namespace: MyAppWeb

  # Controller-level plug: runs before every action in this module.
  # Phoenix resolves the binding at request time via the plug chain;
  # there's no explicit call site between the action and the plug
  # function.
  plug :authenticate

  # Guarded form with `when action in [...]`: same DI shape as Rails's
  # `before_action :name, only: [...]`. The plug only fires for the
  # listed actions.
  plug :load_user when action in [:show, :update, :delete]

  # Regular actions. Bodies deliberately do NOT call authenticate or
  # load_user directly — the only path from these actions to those
  # plugs is the decorator-dispatch edge we want to extract.
  def index(conn, _params) do
    render(conn, :index)
  end

  def show(conn, _params) do
    render(conn, :show, user: conn.assigns[:user])
  end

  def update(conn, params) do
    user = conn.assigns[:user]
    _ = Map.merge(user, params)
    render(conn, :show, user: user)
  end

  def delete(conn, _params) do
    send_resp(conn, 204, "")
  end

  # Plug functions themselves. Their bodies have real call edges (to
  # Plug.Conn helpers etc.) which the existing extractor handles;
  # only the binding from actions to these functions is DI-gap
  # territory.
  def authenticate(conn, _opts) do
    case get_session(conn, :user_id) do
      nil -> halt_unauthorized(conn)
      _ -> conn
    end
  end

  def load_user(conn, _opts) do
    user = %{id: conn.params["id"]}
    assign(conn, :user, user)
  end

  defp halt_unauthorized(conn) do
    send_resp(conn, 401, "unauthorized")
  end
end
