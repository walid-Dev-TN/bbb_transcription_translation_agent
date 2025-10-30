# 🚀 BBB-Translation-Bot

**BBB-Translation-Bot** helps with communication in [BigBlueButton (BBB)](https://bigbluebutton.org/) meetings by providing **real-time transcription and translation**. The bot listens to the meeting’s audio, turns spoken words into text, and shows translations using a self-hosted version of [Whisper AI](https://github.com/openai/whisper) ([faster-whisper](https://github.com/SYSTRAN/faster-whisper)) and [LibreTranslate](https://libretranslate.com/). It automatically adds the text as closed captions in BBB. 📝🌐

> **‼️🚨--> This project only works with BBB 3.0 <--🚨‼️**

---

## 🛠️ Getting Started

### 📋 Prerequisites

Before you begin, ensure you have the following:

- **Hardware:**
  - **CPU:** This version of the project is adapted to cpu environment.
  - **Storage:** Minimum 64GB disk space (more recommended for active development as Docker can consume significant disk space).

- **Software:**
  - **Operating System:**
    - [Ubuntu 22.04](https://releases.ubuntu.com/jammy/) or
    - Windows: [WSL2](https://docs.microsoft.com/en-us/windows/wsl/install) with Ubuntu 22.04.
  - **Access:** Root access to your machine.


### 💻 Developer Setup

If you plan to contribute or customize BBB-Translation-Bot, set up a development environment:

1. **Clone the Repository:**
2. Build the Bot:
   docker compose built
3. configure .env file wiht bbb secret key, your domain name...
4. **Start the Bot:**
run: docker compose up -d

5. **Open web browser:**

    Open your web browser and navigate to:

    ```plaintext
    http://<ip>:9090
    ```

    Replace `<ip>` with your actual domain or IP address.

6. **Logs:**

    To view the logs, run:

    ```bash
    tail -f logs/bot.log
    tail -f logs/changeset-grpc.log
    tail -f logs/prometheus.log
    tail -f logs/transcription-service.log
    tail -f logs/translation-service.log
    ```

## 📚 Additional Resources

- **BigBlueButton:** [Official Website](https://bigbluebutton.org/)
- **Whisper by OpenAI:** [GitHub Repository](https://github.com/openai/whisper)
- **faster-whisper:** [GitHub Repository](https://github.com/SYSTRAN/faster-whisper)
- **whisperX:** [GitHub Repository](https://github.com/m-bain/whisperX)
- **whisper_streaming:** [GitHub Repository](https://github.com/ufal/whisper_streaming)
- **bigbluebutton-bot:** [GitHub Repository](https://github.com/bigbluebutton-bot/bigbluebutton-bot)
- **transcription-service**: [GitHub Repository](https://github.com/bigbluebutton-bot/transcription-service)
- **stream_pipeline:** [GitHub Repository](https://github.com/bigbluebutton-bot/stream_pipeline)
- **changeset-grpc:** [GitHub Repository](https://github.com/bigbluebutton-bot/changeset-grpc)
- **LibreTranslate:** [GitHub Repository](https://github.com/LibreTranslate/LibreTranslate)

---

## 🙏 Contributing

I welcome contributions! Whether it's reporting issues, suggesting features, or submitting pull requests, your help is greatly appreciated. 🤝

---

## 📝 License

This project is licensed under the [MIT License](LICENSE).

---

## 📫 Contact

For any questions or support, feel free to [open an issue](https://github.com/bigbluebutton-bot/bbb-translation-bot/issues) on GitHub.

---
