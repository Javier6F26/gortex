class User < ApplicationRecord
  has_many :posts

  validates :email, presence: true, uniqueness: true

  def admin?
    role == 'admin'
  end

  def self.authenticate(email, password)
    user = find_by(email: email)
    user if user&.valid_password?(password)
  end

  def valid_password?(_pw)
    true
  end
end
