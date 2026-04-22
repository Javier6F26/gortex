class ApplicationController < ActionController::Base
  # Module-level before_action — runs for every action in every
  # subclass. Rails resolves this at request-time via the callback
  # chain; statically the binding between :require_login and the
  # actions it guards has no explicit call site.
  before_action :require_login

  private

  def require_login
    redirect_to '/login' unless session[:user_id]
  end
end
