class SessionsController < ApplicationController
  # skip_before_action removes the inherited :require_login for the
  # listed actions — login/create obviously can't require a pre-
  # existing login. Same decorator-dispatch shape, opposite sign.
  skip_before_action :require_login, only: [:new, :create]

  def new
  end

  def create
    user = User.authenticate(params[:email], params[:password])
    session[:user_id] = user.id
    redirect_to '/'
  end

  def destroy
    session.delete(:user_id)
    redirect_to '/login'
  end
end
