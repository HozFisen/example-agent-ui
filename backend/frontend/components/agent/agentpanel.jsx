"use client";

import { useState, useEffect } from "react";
import { runTask, listUIFiles } from "@/lib/api";
import StepFeed from "./stepfeed";
import InstructionList from "./instructionlist";

export default function AgentPanel() {
  const [prompt, setPrompt] = useState("");
  const [selectedFile, setSelectedFile] = useState("");
  const [uiFiles, setUIFiles] = useState([]);
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState(null);

  // Load available UI files on mount
  useEffect(() => {
    listUIFiles()
      .then(setUIFiles)
      .catch(() => setUIFiles([]));
  }, []);

  async function handleSubmit(e) {
    e.preventDefault();
    if (!prompt.trim()) return;

    setLoading(true);
    setResult(null);
    setError(null);

    try {
      const data = await runTask(prompt, selectedFile);
      setResult(data);
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="max-w-5xl mx-auto p-6 space-y-8">
      {/* Header */}
      <header className="pt-8">
        <h1 className="text-3xl font-bold text-white">UI Analysis Agent</h1>
        <p className="text-gray-400 mt-1">
          Describe a task and the agent will analyze your UI files to generate step-by-step instructions.
        </p>
      </header>

      {/* Input Form */}
      <form onSubmit={handleSubmit} className="space-y-4">
        {/* UI File selector */}
        {uiFiles.length > 0 && (
          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">
              Target UI File <span className="text-gray-500">(optional)</span>
            </label>
            <select
              value={selectedFile}
              onChange={(e) => setSelectedFile(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-gray-100 focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              <option value="">Auto-detect</option>
              {uiFiles.map((f) => (
                <option key={f} value={f}>{f}</option>
              ))}
            </select>
          </div>
        )}

        {/* Prompt input */}
        <div>
          <label className="block text-sm font-medium text-gray-300 mb-1">
            Task Prompt
          </label>
          <textarea
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            placeholder='e.g. "How do I reset my password?" or "Find the login button"'
            rows={3}
            className="w-full bg-gray-800 border border-gray-700 rounded-lg px-4 py-3 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-blue-500 resize-none"
          />
        </div>

        <button
          type="submit"
          disabled={loading || !prompt.trim()}
          className="px-6 py-2.5 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-lg font-medium text-white transition-colors"
        >
          {loading ? "Running agent…" : "Run Agent"}
        </button>
      </form>

      {/* Error display */}
      {error && (
        <div className="bg-red-900/40 border border-red-700 rounded-lg p-4 text-red-300">
          <strong>Error:</strong> {error}
        </div>
      )}

      {/* Results */}
      {result && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {/* Left: Agent reasoning steps */}
          <section>
            <h2 className="text-lg font-semibold text-gray-200 mb-3">
              Agent Reasoning
              <span className="ml-2 text-xs text-gray-500 font-normal">
                {result.tokens_used} tokens
              </span>
            </h2>
            <StepFeed steps={result.steps} />
          </section>

          {/* Right: Final structured instructions */}
          <section>
            <h2 className="text-lg font-semibold text-gray-200 mb-3">
              Generated Instructions
              <span className="ml-2 text-xs text-gray-500 font-normal">
                {result.final_instructions?.length ?? 0} steps
              </span>
            </h2>
            <InstructionList instructions={result.final_instructions} />
          </section>
        </div>
      )}
    </div>
  );
}