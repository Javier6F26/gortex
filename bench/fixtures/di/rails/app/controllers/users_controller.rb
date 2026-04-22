class UsersController < ApplicationController
  # Targeted before_action: only fires for the listed actions. The
  # comma-separated symbol list + `only:` / `except:` filters are the
  # common Rails idiom for attaching methods to a subset of actions.
  before_action :set_user, only: [:show, :update, :destroy]
  before_action :authorize_admin, only: :destroy
  after_action :log_request

  def index
    @users = User.all
  end

  def show
    render json: @user
  end

  def update
    @user.update(user_params)
    render json: @user
  end

  def destroy
    @user.destroy
    head :no_content
  end

  private

  def set_user
    @user = User.find(params[:id])
  end

  def authorize_admin
    head :forbidden unless current_user&.admin?
  end

  def log_request
    Rails.logger.info("request: #{request.path}")
  end

  def user_params
    params.require(:user).permit(:email, :name)
  end
end
