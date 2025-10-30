# simple_vad.py
import numpy as np
from typing import Optional, List, Dict, Any
import data
import logger
import copy

log = logger.setup_logging()

class SimpleVAD:
    """
    Simple Energy-based Voice Activity Detection
    Returns VAD results in the same format as the advanced VAD module
    """

    def __init__(self, energy_threshold: float = 0.01, min_duration: float = 0.1):
        """
        Initialize Energy-based VAD with PROPER thresholds
        """
        self.energy_threshold = energy_threshold
        self.min_duration = min_duration
        self.speech_buffer = []
        self.current_speech_start = None

        log.info(f"🎯 VAD REACTIVATED: threshold={energy_threshold}, min_duration={min_duration}s")

    def __deepcopy__(self, memo):
        return self.__class__(
            energy_threshold=self.energy_threshold,
            min_duration=self.min_duration
        )

    def init_module(self):
        log.debug("VAD module initialized in pipeline")

    def run(self, *args, **kwargs) -> Optional[data.AudioData]:
        try:
            input_data = None
            if args and len(args) > 0:
                input_data = args[0]
            elif 'dp' in kwargs:
                input_data = kwargs['dp']

            if input_data is not None:
                result = self.process(input_data)
                return result
            else:
                log.error("No input_data found in args or kwargs")
                return None

        except Exception as e:
            log.error(f"Error in VAD run method: {e}")
            return None

    def process(self, input_data) -> Optional[data.AudioData]:
        """
        Process audio data using energy-based VAD with ACTUAL FILTERING
        """
        try:
            # Extract audio data
            audio_chunk = None
            original_data = None

            if hasattr(input_data, 'data'):
                original_data = input_data.data
                if hasattr(original_data, 'raw_audio_data'):
                    audio_chunk = original_data.raw_audio_data
                elif hasattr(original_data, 'audio_data'):
                    audio_chunk = original_data.audio_data
            elif hasattr(input_data, 'raw_audio_data'):
                audio_chunk = input_data.raw_audio_data
                original_data = input_data
            elif hasattr(input_data, 'audio_data'):
                audio_chunk = input_data.audio_data
                original_data = input_data

            if audio_chunk is None:
                log.error("No audio data found in input")
                return None

            # Convert to numpy array if it's bytes
            if isinstance(audio_chunk, bytes):
                audio_array = np.frombuffer(audio_chunk, dtype=np.int16).astype(np.float32) / 32768.0
            else:
                audio_array = audio_chunk

            # Calculate RMS energy
            rms = np.sqrt(np.mean(audio_array**2))

            # Calculate audio duration
            sample_rate = 16000
            audio_duration = len(audio_array) / sample_rate

            # 🚨 ACTUAL VAD FILTERING - NO MORE "ALWAYS PASS"
            if rms > self.energy_threshold and audio_duration >= self.min_duration:
                log.info(f"🎤 VAD SPEECH DETECTED: RMS={rms:.6f}, duration={audio_duration:.2f}s")

                # Create VAD result
                vad_segments = [
                    {
                        "start": 0.0,
                        "end": audio_duration,
                        "segments": [(0.0, audio_duration)]
                    }
                ]

                # Set all required fields
                if original_data is not None:
                    original_data.audio_data = audio_array
                    original_data.vad_result = vad_segments
                    original_data.audio_buffer_start_after = 0.0
                    original_data.audio_data_sample_rate = sample_rate
                    original_data.audio_buffer_time = audio_duration

                    if not hasattr(original_data, 'task') or original_data.task is None:
                        original_data.task = data.Task.TRANSCRIBE

                return input_data
            else:
                log.info(f"🔇 VAD REJECTED: RMS={rms:.6f} < threshold={self.energy_threshold:.6f} or duration={audio_duration:.2f}s < min={self.min_duration}s")
                return None  # 🚨 ACTUALLY REJECT NON-SPEECH AUDIO

        except Exception as e:
            log.error(f"VAD processing error: {e}")
            # On error, be conservative and pass audio through
            return input_data
