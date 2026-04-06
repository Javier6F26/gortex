package languages

import (
	"testing"
)

// Go source samples of increasing size.
var goSmall = []byte(`package main

func main() {
	fmt.Println("hello")
}
`)

var goMedium = []byte(`package server

import (
	"context"
	"net/http"
	"time"
)

type Server struct {
	addr   string
	router *http.ServeMux
	logger Logger
}

type Logger interface {
	Info(msg string)
	Error(msg string, err error)
}

func New(addr string, logger Logger) *Server {
	return &Server{addr: addr, router: http.NewServeMux(), logger: logger}
}

func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{Addr: s.addr, Handler: s.router, ReadHeaderTimeout: 10 * time.Second}
	s.logger.Info("starting server")
	return srv.ListenAndServe()
}

func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("stopping server")
	return nil
}

func (s *Server) Handle(pattern string, handler http.Handler) {
	s.router.Handle(pattern, handler)
}

func (s *Server) HandleFunc(pattern string, handler http.HandlerFunc) {
	s.router.HandleFunc(pattern, handler)
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
`)

var tsSource = []byte(`import { Router, Request, Response } from 'express';

interface UserService {
  findById(id: string): Promise<User>;
  save(user: User): Promise<void>;
}

class UserController {
  constructor(private service: UserService) {}

  async getUser(req: Request, res: Response) {
    const user = await this.service.findById(req.params.id);
    res.json(user);
  }

  async createUser(req: Request, res: Response) {
    await this.service.save(req.body);
    res.status(201).send();
  }
}

const router = Router();
export default router;
`)

var pySource = []byte(`import os
from pathlib import Path

class FileProcessor:
    def __init__(self, root_dir):
        self.root_dir = Path(root_dir)

    def process_all(self):
        for f in self.root_dir.rglob("*.txt"):
            self.process_file(f)

    def process_file(self, path):
        content = path.read_text()
        return self.transform(content)

    def transform(self, content):
        return content.upper()

def main():
    processor = FileProcessor("/data")
    processor.process_all()
`)

var rsSource = []byte(`use std::collections::HashMap;

pub struct Cache<T> {
    data: HashMap<String, T>,
    capacity: usize,
}

pub trait Store {
    fn get(&self, key: &str) -> Option<String>;
    fn set(&mut self, key: &str, value: String);
}

impl<T: Clone> Cache<T> {
    pub fn new(capacity: usize) -> Self {
        Cache { data: HashMap::new(), capacity }
    }

    pub fn get(&self, key: &str) -> Option<&T> {
        self.data.get(key)
    }

    pub fn set(&mut self, key: String, value: T) {
        if self.data.len() >= self.capacity {
            return;
        }
        self.data.insert(key, value);
    }
}
`)

var javaSource = []byte(`import java.util.List;
import java.util.Optional;

public interface Repository<T> {
    Optional<T> findById(String id);
    List<T> findAll();
    void save(T entity);
}

public class UserRepository implements Repository<User> {
    private final Database db;

    public UserRepository(Database db) {
        this.db = db;
    }

    public Optional<User> findById(String id) {
        return db.query("SELECT * FROM users WHERE id = ?", id);
    }

    public List<User> findAll() {
        return db.queryAll("SELECT * FROM users");
    }

    public void save(User user) {
        db.execute("INSERT INTO users VALUES (?)", user);
    }
}
`)

var rbSource = []byte(`require "json"

class UserService
  def initialize(repo)
    @repo = repo
  end

  def find_user(id)
    @repo.find(id)
  end

  def create_user(attrs)
    user = User.new(attrs)
    @repo.save(user)
    user
  end

  def delete_user(id)
    @repo.delete(id)
  end
end

module Validators
  def self.validate_email(email)
    email.match?(/\A[\w+\-.]+@[a-z\d\-]+(\.[a-z]+)*\.[a-z]+\z/i)
  end
end
`)

var exSource = []byte(`defmodule MyApp.UserService do
  alias MyApp.Repo
  import Ecto.Query

  def get_user(id) do
    Repo.get(User, id)
  end

  def create_user(attrs) do
    %User{}
    |> User.changeset(attrs)
    |> Repo.insert()
  end

  defp validate(changeset) do
    changeset
  end
end
`)

func BenchmarkGoExtractor_Small(b *testing.B) {
	e := NewGoExtractor()
	for b.Loop() {
		_, _ = e.Extract("main.go", goSmall)
	}
}

func BenchmarkGoExtractor_Medium(b *testing.B) {
	e := NewGoExtractor()
	for b.Loop() {
		_, _ = e.Extract("server.go", goMedium)
	}
}

func BenchmarkTypeScriptExtractor(b *testing.B) {
	e := NewTypeScriptExtractor()
	for b.Loop() {
		_, _ = e.Extract("controller.ts", tsSource)
	}
}

func BenchmarkPythonExtractor(b *testing.B) {
	e := NewPythonExtractor()
	for b.Loop() {
		_, _ = e.Extract("processor.py", pySource)
	}
}

func BenchmarkRustExtractor(b *testing.B) {
	e := NewRustExtractor()
	for b.Loop() {
		_, _ = e.Extract("cache.rs", rsSource)
	}
}

func BenchmarkJavaExtractor(b *testing.B) {
	e := NewJavaExtractor()
	for b.Loop() {
		_, _ = e.Extract("UserRepository.java", javaSource)
	}
}

func BenchmarkRubyExtractor(b *testing.B) {
	e := NewRubyExtractor()
	for b.Loop() {
		_, _ = e.Extract("service.rb", rbSource)
	}
}

func BenchmarkElixirExtractor(b *testing.B) {
	e := NewElixirExtractor()
	for b.Loop() {
		_, _ = e.Extract("user_service.ex", exSource)
	}
}
